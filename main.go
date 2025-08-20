package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"shtrih-kkt/pkg/shtrih"
)

const (
	configFileName   = "connect.json"
	outputDir        = "date"
	comSearchTimeout = 500 * time.Millisecond
	tcpSearchTimeout = 2 * time.Second
)

// ConfigFile соответствует структуре файла connect.json
type ConfigFile struct {
	Timeout int                  `json:"timeout_to_ip_port"`
	Shtrih  []ConnectionSettings `json:"shtrih"`
	Atol    []interface{}        `json:"atol"`
}

// ConnectionSettings описывает один блок настроек подключения для Штрих-М
type ConnectionSettings struct {
	TypeConnect int32  `json:"type_connect"`
	ComPort     string `json:"com_port"`
	ComBaudrate string `json:"com_baudrate"`
	IP          string `json:"ip"`
	IPPort      string `json:"ip_port"`
}

// PolledDevice связывает конфигурацию, использованную для подключения,
// с фискальной информацией, полученной от устройства.
type PolledDevice struct {
	Config shtrih.Config
	Info   *shtrih.FiscalInfo
}

func main() {
	log.Println("Запуск приложения для сбора данных с ККТ Штрих-М...")

	configData, err := os.ReadFile(configFileName)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("Файл конфигурации '%s' не найден. Запускаю режим автопоиска устройств...", configFileName)
			runDiscoveryMode()
		} else {
			log.Fatalf("Ошибка чтения файла конфигурации '%s': %v", configFileName, err)
		}
	} else {
		log.Printf("Найден файл конфигурации '%s'. Запускаю стационарный режим...", configFileName)
		runConfigMode(configData)
	}

	log.Println("Работа приложения завершена.")
}

func runConfigMode(data []byte) {
	var configFile ConfigFile
	if err := json.Unmarshal(data, &configFile); err != nil {
		log.Printf("Ошибка парсинга JSON из '%s': %v. Переключаюсь на режим автопоиска.", configFileName, err)
		runDiscoveryMode()
		return
	}

	if len(configFile.Shtrih) == 0 {
		log.Printf("В файле '%s' не найдено настроек для 'shtrih'. Переключаюсь на режим автопоиска.", configFileName)
		runDiscoveryMode()
		return
	}

	log.Printf("Найдено %d конфигураций для Штрих-М. Начинаю опрос...", len(configFile.Shtrih))
	configs := convertSettingsToConfigs(configFile.Shtrih)
	if len(configs) == 0 {
		log.Println("Не удалось создать ни одной валидной конфигурации из файла. Проверьте данные в connect.json.")
		return
	}

	processDevices(configs)
}

func runDiscoveryMode() {
	configs, err := shtrih.SearchDevices(comSearchTimeout, tcpSearchTimeout)
	if err != nil {
		log.Printf("Во время поиска устройств произошла ошибка: %v", err)
	}

	if len(configs) == 0 {
		log.Println("В ходе сканирования не найдено ни одного устройства Штрих-М.")
		return
	}

	log.Printf("Найдено %d устройств. Начинаю сбор информации...", len(configs))
	polledDevices := processDevices(configs)

	// Если были успешно опрошены какие-либо устройства, сохраняем их конфигурацию
	if len(polledDevices) > 0 {
		saveConfiguration(polledDevices)
	}
}
// processDevices - основная функция, реализующая "умную" логику обновления и создания файлов.
// Теперь она возвращает срез успешно опрошенных устройств.
func processDevices(configs []shtrih.Config) []PolledDevice {
	// Шаг 1: Сначала собираем информацию со всех найденных устройств.
	var polledDevices []PolledDevice // Было: var freshKKTData []*shtrih.FiscalInfo
	for _, config := range configs {
		log.Printf("--- Опрашиваю устройство: %+v ---", config)
		driver := shtrih.New(config)

		if err := driver.Connect(); err != nil {
			log.Printf("Не удалось подключиться к устройству: %v", err)
			continue
		}

		info, err := driver.GetFiscalInfo()
		driver.Disconnect() // Отключаемся сразу после получения данных

		if err != nil {
			log.Printf("Ошибка при получении фискальной информации: %v", err)
			continue
		}
		if info == nil || info.SerialNumber == "" {
			log.Println("Получена пустая информация или отсутствует серийный номер, данные проигнорированы.")
			continue
		}
		// Сохраняем и конфигурацию, и результат
		polledDevices = append(polledDevices, PolledDevice{Config: config, Info: info})
	}

	if len(polledDevices) == 0 {
		log.Println("--- Не удалось собрать данные ни с одного устройства. Завершение. ---")
		return nil // Возвращаем nil, если ничего не найдено
	}

	log.Printf("--- Всего собрано данных с %d устройств. Начинаю обработку файлов. ---", len(polledDevices))

	// Шаг 2: Ищем "донора" данных о рабочей станции в папке /date.
	sourceWSDataMap := findSourceWorkstationData()

	// Шаг 3: Обрабатываем каждого "свежего" ККТ в соответствии с новой логикой.
	var successCount int
	for _, pd := range polledDevices { // Итерируемся по новой структуре
		kktInfo := pd.Info // Получаем доступ к данным ККТ
		fileName := fmt.Sprintf("%s.json", kktInfo.SerialNumber)
		filePath := filepath.Join(outputDir, fileName)

		// ... (остальная часть цикла остается без изменений) ...
		if _, err := os.Stat(filePath); err == nil {
			log.Printf("Файл для ККТ %s уже существует. Обновляю временную метку...", kktInfo.SerialNumber)
			if err := updateTimestampInFile(filePath); err != nil {
				log.Printf("Не удалось обновить файл %s: %v", filePath, err)
			} else {
				log.Printf("Файл %s успешно обновлен.", filePath)
				successCount++
			}
		} else {
			var wsDataToUse map[string]interface{}
			if sourceWSDataMap != nil {
				log.Printf("Создаю новый файл для ККТ %s, используя данные о рабочей станции из файла-донора.", kktInfo.SerialNumber)
				wsDataToUse = sourceWSDataMap
			} else {
				log.Printf("Создаю первичный файл для ККТ %s с базовыми данными о рабочей станции.", kktInfo.SerialNumber)
				hostname, _ := os.Hostname()
				wsDataToUse = map[string]interface{}{"hostname": hostname}
			}
			wsDataToUse["current_time"] = time.Now().Format("2006-01-02 15:04:05")
			if err := saveNewMergedInfo(kktInfo, wsDataToUse, filePath); err != nil {
				log.Printf("Не удалось создать файл для ККТ %s: %v", kktInfo.SerialNumber, err)
			} else {
				successCount++
			}
		}
	}
	log.Printf("--- Обработка файлов завершена. Успешно создано/обновлено: %d файлов. ---", successCount)

	return polledDevices // Возвращаем результат
}
// findSourceWorkstationData ищет в папке /date любой .json файл и извлекает из него
// все данные как `map[string]interface{}`.
func findSourceWorkstationData() map[string]interface{} {
	files, err := os.ReadDir(outputDir)
	if err != nil {
		return nil
	}

	for _, file := range files {
		if !file.IsDir() && filepath.Ext(file.Name()) == ".json" {
			filePath := filepath.Join(outputDir, file.Name())
			data, err := os.ReadFile(filePath)
			if err != nil {
				log.Printf("Предупреждение: не удалось прочитать файл-донор %s: %v", filePath, err)
				continue
			}

			var content map[string]interface{}
			if err := json.Unmarshal(data, &content); err != nil {
				log.Printf("Предупреждение: не удалось распарсить JSON из файла-донора %s: %v", filePath, err)
				continue
			}

			// Проверяем, что это не файл от нашего ККТ (у него не должно быть поля modelName)
			// и что у него есть hostname. Это делает выбор донора более надежным.
			_, hasModelName := content["modelName"]
			_, hasHostname := content["hostname"]
			if !hasModelName && hasHostname {
				log.Printf("Найден файл-донор с данными о рабочей станции: %s", filePath)
				return content // Возвращаем все содержимое файла как карту.
			}
		}
	}

	log.Println("В папке /date не найдено файлов-доноров. Будут использованы базовые данные.")
	return nil
}

// updateTimestampInFile читает JSON-файл, обновляет в нем поле current_time и перезаписывает его.
func updateTimestampInFile(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("ошибка чтения файла: %w", err)
	}

	var content map[string]interface{}
	if err := json.Unmarshal(data, &content); err != nil {
		return fmt.Errorf("ошибка парсинга JSON: %w", err)
	}

	content["current_time"] = time.Now().Format("2006-01-02 15:04:05")

	updatedData, err := json.MarshalIndent(content, "", "    ")
	if err != nil {
		return fmt.Errorf("ошибка маршалинга JSON: %w", err)
	}

	return os.WriteFile(filePath, updatedData, 0644)
}

// saveNewMergedInfo объединяет данные ККТ и данные рабочей станции (в виде map) и сохраняет в новый JSON-файл.
func saveNewMergedInfo(kktInfo *shtrih.FiscalInfo, wsData map[string]interface{}, filePath string) error {
	var kktMap map[string]interface{}
	kktJSON, _ := json.Marshal(kktInfo)
	json.Unmarshal(kktJSON, &kktMap)

	// Сливаем карты. Ключи из wsData перезапишут любые совпадения в kktMap.
	for key, value := range wsData {
		kktMap[key] = value
	}
	
	// Удаляем поля, специфичные для ККТ, из данных донора, если они случайно туда попали.
	// Это предотвратит запись, например, "serialNumber" от АТОЛ в файл Штриха.
	delete(kktMap, "serialNumber")

	// Возвращаем серийный номер нашего ККТ, который мы сохранили в структуре kktInfo.
	kktMap["serialNumber"] = kktInfo.SerialNumber

	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return fmt.Errorf("не удалось создать директорию '%s': %w", outputDir, err)
	}

	finalJSON, err := json.MarshalIndent(kktMap, "", "    ")
	if err != nil {
		return fmt.Errorf("ошибка маршалинга итогового JSON: %w", err)
	}

	if err := os.WriteFile(filePath, finalJSON, 0644); err != nil {
		return fmt.Errorf("ошибка записи в файл '%s': %w", filePath, err)
	}

	log.Printf("Данные для ККТ %s успешно сохранены в новый файл: %s", kktInfo.SerialNumber, filePath)
	return nil
}

// convertSettingsToConfigs преобразует настройки из файла в формат, понятный библиотеке.
func convertSettingsToConfigs(settings []ConnectionSettings) []shtrih.Config {
	var configs []shtrih.Config
	baudRateMap := map[string]int32{
		"115200": 6, "57600": 5, "38400": 4, "19200": 3, "9600": 2,
	}

	for _, s := range settings {
		config := shtrih.Config{
			ConnectionType: s.TypeConnect,
			Password:       30,
		}
		switch s.TypeConnect {
		case 0: // COM-порт
			comNum, err := strconv.Atoi(s.ComPort[3:])
			if err != nil {
				log.Printf("Некорректное имя COM-порта '%s' в конфигурации, пропуск.", s.ComPort)
				continue
			}
			baudRate, ok := baudRateMap[s.ComBaudrate]
			if !ok {
				log.Printf("Некорректная скорость '%s' для порта '%s', пропуск.", s.ComBaudrate, s.ComPort)
				continue
			}
			config.ComName = s.ComPort
			config.ComNumber = int32(comNum)
			config.BaudRate = baudRate
		case 6: // TCP/IP
			port, err := strconv.Atoi(s.IPPort)
			if err != nil {
				log.Printf("Некорректный TCP-порт '%s' для IP '%s', пропуск.", s.IPPort, s.IP)
				continue
			}
			config.IPAddress = s.IP
			config.TCPPort = int32(port)
		default:
			log.Printf("Неизвестный тип подключения '%d', пропуск.", s.TypeConnect)
			continue
		}
		configs = append(configs, config)
	}
	return configs
}

// saveConfiguration обновляет файл connect.json, записывая в него
// конфигурации успешно найденных и опрошенных устройств.
func saveConfiguration(polledDevices []PolledDevice) {
	log.Printf("Сохранение %d найденных конфигураций в файл '%s'...", len(polledDevices), configFileName)

	// Шаг 1: Читаем существующий файл или создаем новую структуру.
	var configFile ConfigFile
	data, err := os.ReadFile(configFileName)
	if err == nil {
		// Файл есть, парсим его, чтобы не потерять другие секции (например, "atol")
		if err := json.Unmarshal(data, &configFile); err != nil {
			log.Printf("Предупреждение: файл '%s' поврежден (%v). Он будет перезаписан.", configFileName, err)
			configFile = ConfigFile{} // Создаем пустую структуру в случае ошибки
		}
	}

	// Шаг 2: Формируем новый срез настроек для "shtrih".
	var newShtrihSettings []ConnectionSettings
	for _, pd := range polledDevices {
		newShtrihSettings = append(newShtrihSettings, convertConfigToSettings(pd.Config))
	}

	// Шаг 3: Полностью заменяем секцию "shtrih" новыми данными.
	configFile.Shtrih = newShtrihSettings

	// Шаг 4: Записываем обновленную структуру обратно в файл.
	updatedData, err := json.MarshalIndent(configFile, "", "    ")
	if err != nil {
		log.Printf("Ошибка: не удалось преобразовать конфигурацию в JSON: %v", err)
		return
	}

	if err := os.WriteFile(configFileName, updatedData, 0644); err != nil {
		log.Printf("Ошибка: не удалось записать конфигурацию в файл '%s': %v", configFileName, err)
		return
	}

	log.Printf("Конфигурация успешно сохранена в '%s'.", configFileName)
}

// convertConfigToSettings преобразует внутренний формат shtrih.Config
// в формат ConnectionSettings для записи в connect.json.
func convertConfigToSettings(config shtrih.Config) ConnectionSettings {
	// Карта для обратного преобразования индекса скорости в строку
	baudRateReverseMap := map[int32]string{
		6: "115200", 5: "57600", 4: "38400", 3: "19200", 2: "9600",
	}

	settings := ConnectionSettings{
		TypeConnect: config.ConnectionType,
	}

	switch config.ConnectionType {
	case 0: // COM-порт
		settings.ComPort = config.ComName
		settings.ComBaudrate = baudRateReverseMap[config.BaudRate]
	case 6: // TCP/IP
		settings.IP = config.IPAddress
		settings.IPPort = strconv.Itoa(int(config.TCPPort))
	}
	return settings
}