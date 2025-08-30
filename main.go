package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"shtrih-kkt/pkg/shtrih"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	configFileName    = "connect.json"
	serviceConfigName = "service.json"
	logsDir           = "logs"
	comSearchTimeout  = 200 * time.Millisecond
	tcpSearchTimeout  = 200 * time.Millisecond
)

// Глобальная переменная для пути вывода. Это позволяет подменять ее в тестах.
var (
	outputDir = "date"
	version   = "0.1.4-dev"
)

// --- СТРУКТУРЫ ДЛЯ ПАРСИНГА КОНФИГУРАЦИОННЫХ ФАЙЛОВ ---

type ConfigFile struct {
	Timeout int                  `json:"timeout_to_ip_port"`
	Shtrih  []ConnectionSettings `json:"shtrih"`
	Atol    []interface{}        `json:"atol"`
}

type ConnectionSettings struct {
	TypeConnect int32  `json:"type_connect"`
	ComPort     string `json:"com_port"`
	ComBaudrate string `json:"com_baudrate"`
	IP          string `json:"ip"`
	IPPort      string `json:"ip_port"`
}

type ServiceFile struct {
	Service ServiceConfig `json:"service"`
}

type ServiceConfig struct {
	LogLevel  string `json:"log_level"`
	LogDays   int    `json:"log_days"`
	UpdateURL string `json:"update_url"`
}

type PolledDevice struct {
	Config shtrih.Config
	Info   *shtrih.FiscalInfo
}

// --- ОСНОВНАЯ ЛОГИКА ПРИЛОЖЕНИЯ ---

func main() {
	log.Printf("Запуск приложения для сбора данных с ККТ Штрих-М, версия: %s", version)

	// Загружаем сервисную конфигурацию в самом начале.
	serviceConfig := loadServiceConfig()

	// Настраиваем файловое логирование.
	setupLogger(serviceConfig)

	// В фоне запускаем проверку обновлений, если URL указан.
	if serviceConfig != nil {
		go checkForUpdates(version, serviceConfig.UpdateURL)
	}

	// Основная логика приложения.
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

// loadServiceConfig читает и парсит service.json.
// Возвращает конфигурацию или nil, если файл не найден или поврежден.
func loadServiceConfig() *ServiceConfig {
	data, err := os.ReadFile(serviceConfigName)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Предупреждение: ошибка чтения файла '%s': %v.", serviceConfigName, err)
		}
		return nil
	}

	var serviceFile ServiceFile
	if err := json.Unmarshal(data, &serviceFile); err != nil {
		log.Printf("Предупреждение: не удалось прочитать настройки из '%s' (%v).", serviceConfigName, err)
		return nil
	}
	return &serviceFile.Service
}

func setupLogger(config *ServiceConfig) {
	if config == nil {
		log.Printf("Предупреждение: файл настроек '%s' не найден или некорректен. Логирование продолжится в консоль.", serviceConfigName)
		return
	}

	logDays := config.LogDays
	if logDays <= 0 {
		logDays = 7 // Значение по умолчанию
	}

	if err := os.MkdirAll(logsDir, 0755); err != nil {
		log.Printf("Ошибка создания директории для логов '%s': %v. Логирование продолжится в консоль.", logsDir, err)
		return
	}

	logFilePath := filepath.Join(logsDir, "shtrih-scanner.log")

	lumberjackLogger := &lumberjack.Logger{
		Filename:   logFilePath,
		MaxSize:    5,
		MaxBackups: 10,
		MaxAge:     logDays,
		Compress:   true,
	}

	log.SetOutput(io.MultiWriter(os.Stdout, lumberjackLogger))
	log.Printf("Логирование настроено. Уровень: %s, ротация: %d дней. Файл: %s", config.LogLevel, logDays, logFilePath)
}

func runConfigMode(data []byte) {
	// setupLogger() // <--- УДАЛИТЕ ЭТУ СТРОКУ

	var configFile ConfigFile
	if err := json.Unmarshal(data, &configFile); err != nil {
		log.Printf("Ошибка парсинга JSON из '%s': %v. Переключаюсь на режим автопоиска.", configFileName, err)
		runDiscoveryMode()
		return
	}

	// Проверяем наличие секции shtrih в файле, но не пустоту массива.
	// Пустой массив shtrih: [] является валидным состоянием.
	if configFile.Shtrih == nil {
		log.Printf("В файле '%s' отсутствует секция 'shtrih'. Переключаюсь на режим автопоиска.", configFileName)
		runDiscoveryMode()
		return
	}

	if len(configFile.Shtrih) == 0 {
		log.Println("Список устройств 'shtrih' в конфигурации пуст. Сканирование не требуется.")
		// Здесь можно завершить работу, так как опрашивать нечего.
		return
	}

	log.Printf("Найдено %d конфигураций для Штрих-М. Начинаю опрос...", len(configFile.Shtrih))
	configs := convertSettingsToConfigs(configFile.Shtrih)
	if len(configs) == 0 {
		log.Println("Не удалось создать ни одной валидной конфигурации из файла. Проверьте данные в connect.json.")
		return
	}

	// Передаем конструктор реального драйвера shtrih.New
	processDevices(configs, shtrih.New)
}

func runDiscoveryMode() {
	configs, err := shtrih.SearchDevices(comSearchTimeout, tcpSearchTimeout)
	if err != nil {
		log.Printf("Во время поиска устройств произошла ошибка: %v", err)
	}

	if len(configs) == 0 {
		log.Println("В ходе сканирования не найдено ни одного устройства Штрих-М.")
		// Сохраняем информацию об отсутствии устройств, чтобы не сканировать в следующий раз.
		saveEmptyShtrihConfig()
		return // Завершаем работу, так как устройств нет.
	}

	log.Printf("Найдено %d устройств. Начинаю сбор информации...", len(configs))
	// Передаем конструктор реального драйвера shtrih.New
	polledDevices := processDevices(configs, shtrih.New)

	if len(polledDevices) > 0 {
		saveConfiguration(polledDevices)
	}
}

// processDevices принимает функцию-фабрику `newDriverFunc` для создания драйвера.
// Это позволяет подменять реальный драйвер на мок-драйвер в тестах.
func processDevices(configs []shtrih.Config, newDriverFunc func(shtrih.Config) shtrih.Driver) []PolledDevice {
	var polledDevices []PolledDevice
	for _, config := range configs {
		log.Printf("--- Опрашиваю устройство: %+v ---", config)
		// Используем переданную функцию-фабрику для создания драйвера
		driver := newDriverFunc(config)

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

	cleanupDateDirectory()

	var successCount int
	for _, pd := range polledDevices {
		kktInfo := pd.Info
		fileName := fmt.Sprintf("%s.json", kktInfo.SerialNumber)
		filePath := filepath.Join(outputDir, fileName)

		// Определяем, какие данные о рабочей станции использовать.
		var wsDataToUse map[string]interface{}
		if sourceWSDataMap != nil {
			log.Printf("Готовлю данные для ККТ %s, используя информацию из файла-донора.", kktInfo.SerialNumber)
			wsDataToUse = sourceWSDataMap
		} else {
			log.Printf("Готовлю данные для ККТ %s с базовой информацией о рабочей станции (донор не найден).", kktInfo.SerialNumber)
			hostname, _ := os.Hostname()
			wsDataToUse = map[string]interface{}{"hostname": hostname}
		}

		// Безусловно сохраняем/перезаписываем файл.
		if err := saveNewMergedInfo(kktInfo, wsDataToUse, filePath); err != nil {
			log.Printf("Не удалось создать/перезаписать файл для ККТ %s: %v", kktInfo.SerialNumber, err)
		} else {
			// Логика в saveNewMergedInfo уже выводит сообщение об успехе.
			successCount++
		}
	}
	log.Printf("--- Обработка файлов завершена. Успешно создано/обновлено: %d файлов. ---", successCount)

	return polledDevices
}

// --- ФУНКЦИИ ДЛЯ РАБОТЫ С ФАЙЛАМИ ---

// findSourceWorkstationData ищет в папке /date файл с данными о рабочей станции.
// Логика поиска:
// 1. Ищет "идеальный" донор: файл с "hostname", но без "modelName". Если находит - сразу возвращает его.
// 2. Если идеальный не найден, ищет "первый подходящий": любой файл с "hostname", даже если там есть "modelName".
func findSourceWorkstationData() map[string]interface{} {
	files, err := os.ReadDir(outputDir)
	if err != nil {
		return nil
	}

	var firstCandidate map[string]interface{} // Переменная для хранения "первого подходящего" кандидата

	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}

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

		// Проверяем наличие ключевых полей
		_, hasModelName := content["modelName"]
		_, hasHostname := content["hostname"]

		// Если у файла нет hostname, он нам точно не интересен
		if !hasHostname {
			continue
		}

		// Сценарий 1: Найден "идеальный" донор (без modelName)
		if !hasModelName {
			log.Printf("Найден идеальный файл-донор с данными о рабочей станции: %s", filePath)
			return content // Сразу возвращаем его
		}

		// Сценарий 2: Файл не идеальный, но подходит как кандидат (есть и hostname, и modelName)
		// Сохраняем только самого первого кандидата из списка файлов.
		if firstCandidate == nil {
			firstCandidate = content
			log.Printf("Найден файл-кандидат на роль донора (будет использован, если не найдется идеальный): %s", filePath)
		}
	}

	// После проверки всех файлов, если мы так и не вернули идеального донора,
	// используем первого подходящего кандидата, которого нашли.
	if firstCandidate != nil {
		log.Println("Идеальный донор не найден, используется первый подходящий файл-кандидат.")
		return firstCandidate
	}

	// Если мы дошли до сюда, значит не было найдено ни одного файла с полем "hostname".
	log.Println("В папке /date не найдено файлов-доноров. Будут использованы базовые данные.")
	return nil
}

// cleanupDateDirectory сканирует рабочую директорию и удаляет файлы,
// имя которых (без расширения) содержит нечисловые символы.
// Это необходимо для очистки временных/донорских файлов перед записью актуальных данных.
func cleanupDateDirectory() {
	log.Println("Запуск очистки рабочей директории от временных файлов...")

	files, err := os.ReadDir(outputDir)
	if err != nil {
		// Если директория еще не создана, это не ошибка. Просто выходим.
		if os.IsNotExist(err) {
			log.Printf("Директория '%s' не найдена, очистка не требуется.", outputDir)
			return
		}
		log.Printf("Ошибка чтения директории '%s' при очистке: %v", outputDir, err)
		return
	}

	// Регулярное выражение для проверки, что строка состоит только из цифр.
	isNumeric := regexp.MustCompile(`^[0-9]+$`).MatchString
	deletedCount := 0

	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}

		// Получаем имя файла без расширения .json
		baseName := strings.TrimSuffix(file.Name(), filepath.Ext(file.Name()))

		if !isNumeric(baseName) {
			filePath := filepath.Join(outputDir, file.Name())
			log.Printf("Обнаружен некорректный файл '%s'. Удаляю...", file.Name())
			if err := os.Remove(filePath); err != nil {
				log.Printf("Не удалось удалить файл '%s': %v", filePath, err)
			} else {
				log.Printf("Файл '%s' успешно удален.", file.Name())
				deletedCount++
			}
		}
	}

	if deletedCount > 0 {
		log.Printf("Очистка завершена. Удалено %d файлов.", deletedCount)
	} else {
		log.Println("Некорректных файлов для удаления не найдено.")
	}
}

// saveNewMergedInfo объединяет данные ККТ и данные рабочей станции (в виде map) и сохраняет в новый JSON-файл.
// Данные от ККТ имеют приоритет и перезаписывают одноименные поля из данных донора.
func saveNewMergedInfo(kktInfo *shtrih.FiscalInfo, wsData map[string]interface{}, filePath string) error {
	// Шаг 1: Преобразуем данные от нашего ККТ (Штрих) в map.
	var kktMap map[string]interface{}
	kktJSON, _ := json.Marshal(kktInfo)
	json.Unmarshal(kktJSON, &kktMap)

	// Шаг 2: Создаем итоговую карту. Начинаем с данных донора, чтобы они были "внизу".
	// Мы делаем копию wsData, чтобы не изменять оригинальную карту, которая может быть использована в других итерациях.
	finalMap := make(map[string]interface{})
	for key, value := range wsData {
		finalMap[key] = value
	}

	// Шаг 3: "Накладываем" данные от нашего ККТ поверх.
	// Все совпадающие ключи будут перезаписаны значениями от Штриха.
	for key, value := range kktMap {
		// Пропускаем пустые значения от ККТ, чтобы случайно не затереть
		// хорошее значение из донора пустым.
		if s, ok := value.(string); ok && s == "" {
			continue
		}
		finalMap[key] = value
	}

	// Шаг 4: Устанавливаем актуальные временные метки. Они всегда должны быть свежими.
	currentTime := time.Now().Format("2006-01-02 15:04:05")
	finalMap["current_time"] = currentTime
	finalMap["v_time"] = currentTime

	// Шаг 5: Создаем директорию и сохраняем файл.
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return fmt.Errorf("не удалось создать директорию '%s': %w", outputDir, err)
	}

	finalJSON, err := json.MarshalIndent(finalMap, "", "    ")
	if err != nil {
		return fmt.Errorf("ошибка маршалинга итогового JSON: %w", err)
	}

	if err := os.WriteFile(filePath, finalJSON, 0644); err != nil {
		return fmt.Errorf("ошибка записи в файл '%s': %w", filePath, err)
	}

	log.Printf("Данные для ККТ %s успешно сохранены в новый файл: %s", kktInfo.SerialNumber, filePath)
	return nil
}

// saveEmptyShtrihConfig создает или обновляет connect.json, указывая,
// что устройства "Штрих-М" не были найдены. Это предотвращает повторные
// полные сканирования при последующих запусках.
func saveEmptyShtrihConfig() {
	log.Printf("Сохраняю конфигурацию с пустым списком устройств Штрих-М в '%s'...", configFileName)
	var configFile ConfigFile

	// Пытаемся прочитать существующий файл, чтобы не затереть другие секции (например, 'atol').
	data, err := os.ReadFile(configFileName)
	if err == nil {
		if err := json.Unmarshal(data, &configFile); err != nil {
			log.Printf("Предупреждение: файл '%s' поврежден (%v). Он будет перезаписан.", configFileName, err)
			// В случае ошибки парсинга, начинаем с пустой структуры, чтобы исправить файл.
			configFile = ConfigFile{}
		}
	} else if !os.IsNotExist(err) {
		// Логируем ошибку, если это не "файл не найден".
		log.Printf("Предупреждение: не удалось прочитать '%s' (%v). Файл будет создан заново.", configFileName, err)
	}

	// Устанавливаем пустой срез для 'shtrih'.
	configFile.Shtrih = []ConnectionSettings{}

	// Маршалинг и запись обратно в файл.
	updatedData, err := json.MarshalIndent(configFile, "", "    ")
	if err != nil {
		log.Printf("Ошибка: не удалось преобразовать пустую конфигурацию в JSON: %v", err)
		return
	}
	if err := os.WriteFile(configFileName, updatedData, 0644); err != nil {
		log.Printf("Ошибка: не удалось записать пустую конфигурацию в файл '%s': %v", configFileName, err)
		return
	}
	log.Printf("Файл '%s' успешно обновлен с отметкой об отсутствии устройств Штрих-М.", configFileName)
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
