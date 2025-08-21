package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"shtrih-kkt/pkg/shtrih"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	configFileName    = "connect.json"
	serviceConfigName = "service.json"
	outputDir         = "date"
	logsDir           = "logs"
	comSearchTimeout  = 200 * time.Millisecond // Уменьшенный таймаут
	tcpSearchTimeout  = 150 * time.Millisecond
)

// --- СТРУКТУРЫ ДЛЯ ПАРСИНГА КОНФИГУРАЦИОННЫХ ФАЙЛОВ ---

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

// ServiceFile используется для чтения настроек логирования из service.json
type ServiceFile struct {
	Service ServiceConfig `json:"service"`
}

// ServiceConfig содержит параметры логирования
type ServiceConfig struct {
	LogLevel string `json:"log_level"`
	LogDays  int    `json:"log_days"`
}

// PolledDevice связывает конфигурацию, использованную для подключения,
// с фискальной информацией, полученной от устройства.
type PolledDevice struct {
	Config shtrih.Config
	Info   *shtrih.FiscalInfo
}

// --- ОСНОВНАЯ ЛОГИКА ПРИЛОЖЕНИЯ ---

func main() {
	log.Println("Запуск приложения для сбора данных с ККТ Штрих-М...")

	configData, err := os.ReadFile(configFileName)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("Файл конфигурации '%s' не найден. Запускаю режим автопоиска...", configFileName)
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

// setupLogger настраивает запись логов в файл для стационарного режима.
func setupLogger() {
	data, err := os.ReadFile(serviceConfigName)
	if err != nil {
		log.Printf("Предупреждение: файл настроек '%s' не найден. Логирование продолжится в консоль.", serviceConfigName)
		return
	}

	var serviceFile ServiceFile
	if err := json.Unmarshal(data, &serviceFile); err != nil {
		log.Printf("Предупреждение: не удалось прочитать настройки из '%s' (%v). Логирование продолжится в консоль.", serviceConfigName, err)
		return
	}

	// Устанавливаем значения по умолчанию, если в файле их нет
	logDays := serviceFile.Service.LogDays
	if logDays <= 0 {
		logDays = 7
	}

	// Создаем папку для логов, если ее нет
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		log.Printf("Ошибка создания директории для логов '%s': %v. Логирование продолжится в консоль.", logsDir, err)
		return
	}

	logFilePath := filepath.Join(logsDir, "shtrih-scanner.log")

	// Настраиваем ротацию логов
	lumberjackLogger := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    5, // мегабайты
		MaxBackups: 10,
		MaxAge:     logDays, // дни
		Compress:   true,    // сжимать старые файлы
	}

	// Устанавливаем вывод логов и в файл, и в консоль
	log.SetOutput(io.MultiWriter(os.Stdout, lumberjackLogger))
	log.Printf("Логирование настроено. Уровень: %s, ротация: %d дней. Файл: %s", serviceFile.Service.LogLevel, logDays, logFilePath)
}

func runConfigMode(data []byte) {
	// Первым делом настраиваем логирование для стационарного режима!
	setupLogger()

	var configFile ConfigFile
	if err := json.Unmarshal(data, &configFile); err != nil {
		log.Printf("Ошибка парсинга JSON из '%s': %v. Переключаюсь на режим автопоиска.", configFileName, err)
		runDiscoveryMode() // В случае ошибки автопоиск будет логировать только в консоль
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

	if len(polledDevices) > 0 {
		saveConfiguration(polledDevices)
	}
}

func processDevices(configs []shtrih.Config) []PolledDevice {
	var polledDevices []PolledDevice
	for _, config := range configs {
		log.Printf("--- Опрашиваю устройство: %+v ---", config)
		driver := shtrih.New(config)

		if err := driver.Connect(); err != nil {
			log.Printf("Не удалось подключиться к устройству: %v", err)
			continue
		}

		info, err := driver.GetFiscalInfo()
		driver.Disconnect()

		if err != nil {
			log.Printf("Ошибка при получении фискальной информации: %v", err)
			continue
		}
		if info == nil || info.SerialNumber == "" {
			log.Println("Получена пустая информация или отсутствует серийный номер, данные проигнорированы.")
			continue
		}
		polledDevices = append(polledDevices, PolledDevice{Config: config, Info: info})
	}

	if len(polledDevices) == 0 {
		log.Println("--- Не удалось собрать данные ни с одного устройства. Завершение. ---")
		return nil
	}

	log.Printf("--- Всего собрано данных с %d устройств. Начинаю обработку файлов. ---", len(polledDevices))

	sourceWSDataMap := findSourceWorkstationData()

	var successCount int
	for _, pd := range polledDevices {
		kktInfo := pd.Info
		fileName := fmt.Sprintf("%s.json", kktInfo.SerialNumber)
		filePath := filepath.Join(outputDir, fileName)

		if _, err := os.Stat(filePath); err == nil {
			log.Printf("Файл для ККТ %s уже существует. Обновляю временные метки...", kktInfo.SerialNumber)
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

			if err := saveNewMergedInfo(kktInfo, wsDataToUse, filePath); err != nil {
				log.Printf("Не удалось создать файл для ККТ %s: %v", kktInfo.SerialNumber, err)
			} else {
				successCount++
			}
		}
	}
	log.Printf("--- Обработка файлов завершена. Успешно создано/обновлено: %d файлов. ---", successCount)

	return polledDevices
}

// --- ФУНКЦИИ ДЛЯ РАБОТЫ С ФАЙЛАМИ ---

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

			_, hasModelName := content["modelName"]
			_, hasHostname := content["hostname"]
			if !hasModelName && hasHostname {
				log.Printf("Найден файл-донор с данными о рабочей станции: %s", filePath)
				return content
			}
		}
	}

	log.Println("В папке /date не найдено файлов-доноров. Будут использованы базовые данные.")
	return nil
}

func updateTimestampInFile(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("ошибка чтения файла: %w", err)
	}

	var content map[string]interface{}
	if err := json.Unmarshal(data, &content); err != nil {
		return fmt.Errorf("ошибка парсинга JSON: %w", err)
	}

	// Обновляем оба поля времени
	currentTime := time.Now().Format("2006-01-02 15:04:05")
	content["current_time"] = currentTime
	content["v_time"] = currentTime

	updatedData, err := json.MarshalIndent(content, "", "    ")
	if err != nil {
		return fmt.Errorf("ошибка маршалинга JSON: %w", err)
	}

	return os.WriteFile(filePath, updatedData, 0644)
}

func saveNewMergedInfo(kktInfo *shtrih.FiscalInfo, wsData map[string]interface{}, filePath string) error {
	var kktMap map[string]interface{}
	kktJSON, _ := json.Marshal(kktInfo)
	json.Unmarshal(kktJSON, &kktMap)

	// Добавляем актуальные временные метки в данные рабочей станции
	currentTime := time.Now().Format("2006-01-02 15:04:05")
	wsData["current_time"] = currentTime
	wsData["v_time"] = currentTime

	for key, value := range wsData {
		kktMap[key] = value
	}

	delete(kktMap, "serialNumber")
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

func saveConfiguration(polledDevices []PolledDevice) {
	log.Printf("Сохранение %d найденных конфигураций в файл '%s'...", len(polledDevices), configFileName)
	var configFile ConfigFile
	data, err := os.ReadFile(configFileName)
	if err == nil {
		if err := json.Unmarshal(data, &configFile); err != nil {
			log.Printf("Предупреждение: файл '%s' поврежден (%v). Он будет перезаписан.", configFileName, err)
			configFile = ConfigFile{}
		}
	}

	var newShtrihSettings []ConnectionSettings
	for _, pd := range polledDevices {
		newShtrihSettings = append(newShtrihSettings, convertConfigToSettings(pd.Config))
	}

	configFile.Shtrih = newShtrihSettings

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

// --- ВСПОМОГАТЕЛЬНЫЕ ФУНКЦИИ ---

func convertSettingsToConfigs(settings []ConnectionSettings) []shtrih.Config {
	var configs []shtrih.Config
	baudRateMap := map[string]int32{
		"115200": 6, "57600": 5, "38400": 4, "19200": 3, "9600": 2, "4800": 1,
	}

	for _, s := range settings {
		config := shtrih.Config{ConnectionType: s.TypeConnect, Password: 30}
		switch s.TypeConnect {
		case 0:
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
		case 6:
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

func convertConfigToSettings(config shtrih.Config) ConnectionSettings {
	baudRateReverseMap := map[int32]string{
		6: "115200", 5: "57600", 4: "38400", 3: "19200", 2: "9600", 1: "4800",
	}

	settings := ConnectionSettings{TypeConnect: config.ConnectionType}

	switch config.ConnectionType {
	case 0:
		settings.ComPort = config.ComName
		settings.ComBaudrate = baudRateReverseMap[config.BaudRate]
	case 6:
		settings.IP = config.IPAddress
		settings.IPPort = strconv.Itoa(int(config.TCPPort))
	}
	return settings
}
