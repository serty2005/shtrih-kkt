// Package shtrih предоставляет интерфейс для взаимодействия с фискальными
// регистраторами "Штрих-М" через нативный COM-драйвер.
package shtrih

import (
	"fmt"
	"log"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"go.bug.st/serial"
)

// Config определяет параметры для подключения к ККТ.
type Config struct {
	// Тип подключения: 0 для COM-порта, 6 для TCP/IP.
	ConnectionType int32 `json:"connectionType"`
	// IP-адрес устройства (для TCP/IP).
	IPAddress string `json:"ipAddress,omitempty"`
	// TCP-порт устройства (для TCP/IP).
	TCPPort int32 `json:"tcpPort,omitempty"`
	// Имя COM-порта, например, "COM3".
	ComName string `json:"comName,omitempty"`
	// Номер COM-порта (извлекается из ComName).
	ComNumber int32 `json:"-"`
	// Индекс скорости COM-порта (0-6), используемый драйвером.
	BaudRate int32 `json:"baudRate,omitempty"`
	// Пароль для подключения (по умолчанию 30).
	Password int32 `json:"-"`
}

// FiscalInfo содержит агрегированную информацию о фискальном регистраторе.
type FiscalInfo struct {
	ModelName        string `json:"modelName"`          // Наименование модели ККТ
	SerialNumber     string `json:"serialNumber"`       // Заводской номер ККТ
	RNM              string `json:"RNM"`                // Регистрационный номер машины (РНМ)
	OrganizationName string `json:"organizationName"`   // Наименование организации пользователя
	Address          string `json:"address"`            // Адрес установки ККТ
	Inn              string `json:"INN"`                // ИНН пользователя
	FnSerial         string `json:"fn_serial"`          // Серийный номер фискального накопителя
	RegistrationDate string `json:"datetime_reg"`       // Дата и время регистрации ККТ
	FnEndDate        string `json:"dateTime_end"`       // Дата окончания срока действия ФН
	OfdName          string `json:"ofdName"`            // Наименование ОФД
	SoftwareDate     string `json:"bootVersion"`        // Версия (дата) прошивки ККТ
	FfdVersion       string `json:"ffdVersion"`         // Версия ФФД
	FnExecution      string `json:"fnExecution"`        // Исполнение ФН
	InstalledDriver  string `json:"installed_driver"`   // Версия установленного COM-драйвера
	AttributeExcise  bool   `json:"attribute_excise"`   // Признак торговли подакцизными товарами
	AttributeMarked  bool   `json:"attribute_marked"`   // Признак торговли маркированными товарами
	SubscriptionInfo string `json:"licenses,omitempty"` // Строка с лицензиями в расшифрованном виде
}

// Driver определяет основной интерфейс для работы с ККТ.
type Driver interface {
	// Connect устанавливает соединение с ККТ.
	Connect() error
	// Disconnect разрывает соединение с ККТ.
	Disconnect() error
	// GetFiscalInfo собирает и возвращает полную информацию о ККТ.
	GetFiscalInfo() (*FiscalInfo, error)
}

// comDriver является реализацией интерфейса Driver для работы через COM.
type comDriver struct {
	config    Config
	dispatch  *ole.IDispatch
	connected bool
}

// New создает новый экземпляр драйвера с указанной конфигурацией.
func New(config Config) Driver {
	return &comDriver{config: config}
}

// Connect инициализирует COM-объект и устанавливает соединение с ККТ.
// Важно: эта операция должна выполняться в заблокированном потоке ОС из-за
// особенностей работы COM (Single-Threaded Apartment).
func (d *comDriver) Connect() error {
	if d.connected {
		return nil
	}
	runtime.LockOSThread()
	// Инициализация COM-библиотеки для текущего потока.
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		if err := ole.CoInitialize(0); err != nil {
			runtime.UnlockOSThread()
			return fmt.Errorf("COM init failed: %w", err)
		}
	}

	// Создание COM-объекта драйвера "Штрих-М".
	unknown, err := oleutil.CreateObject("AddIn.DrvFR")
	if err != nil {
		ole.CoUninitialize()
		runtime.UnlockOSThread()
		return fmt.Errorf("create COM object failed: %w", err)
	}

	// Получение интерфейса IDispatch для взаимодействия с объектом.
	d.dispatch, err = unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		unknown.Release()
		ole.CoUninitialize()
		runtime.UnlockOSThread()
		return fmt.Errorf("query interface failed: %w", err)
	}
	unknown.Release()

	// Установка свойств подключения в зависимости от типа.
	oleutil.PutProperty(d.dispatch, "ConnectionType", d.config.ConnectionType)
	oleutil.PutProperty(d.dispatch, "Password", d.config.Password)
	switch d.config.ConnectionType {
	case 0: // COM-порт
		oleutil.PutProperty(d.dispatch, "ComNumber", d.config.ComNumber)
		oleutil.PutProperty(d.dispatch, "BaudRate", d.config.BaudRate)
	case 6: // TCP/IP
		oleutil.PutProperty(d.dispatch, "IPAddress", d.config.IPAddress)
		oleutil.PutProperty(d.dispatch, "TCPPort", d.config.TCPPort)
		oleutil.PutProperty(d.dispatch, "UseIPAddress", true)
	}

	// Вызов метода Connect самого COM-объекта.
	if _, err := oleutil.CallMethod(d.dispatch, "Connect"); err != nil {
		d.Disconnect()
		return fmt.Errorf("connect call failed: %w", err)
	}
	// Проверка кода ошибки, возвращаемого драйвером.
	if err := d.checkError(); err != nil {
		d.Disconnect()
		return fmt.Errorf("driver error on connect: %w", err)
	}

	d.connected = true
	log.Println("Подключение к ККТ успешно установлено.")
	return nil
}

// Disconnect разрывает соединение, освобождает COM-ресурсы и разблокирует поток ОС.
func (d *comDriver) Disconnect() error {
	if !d.connected {
		return nil
	}
	oleutil.CallMethod(d.dispatch, "Disconnect")
	d.dispatch.Release()
	ole.CoUninitialize()
	runtime.UnlockOSThread()
	d.connected = false
	log.Println("Соединение с ККТ разорвано.")
	return nil
}

// GetFiscalInfo является orchestrator-методом, который последовательно вызывает
// приватные методы для сбора различных частей информации о ККТ.
func (d *comDriver) GetFiscalInfo() (*FiscalInfo, error) {
	if !d.connected {
		return nil, fmt.Errorf("драйвер не подключен")
	}
	info := &FiscalInfo{}
	var err error
	if err = d.getBaseDeviceInfo(info); err != nil {
		return nil, fmt.Errorf("ошибка получения базовой информации об устройстве: %w", err)
	}
	if err = d.getFiscalizationInfo(info); err != nil {
		return nil, fmt.Errorf("ошибка получения информации о фискализации: %w", err)
	}
	if err = d.getFnInfo(info); err != nil {
		return nil, fmt.Errorf("ошибка получения информации о ФН: %w", err)
	}
	if err = d.getInfoFromTables(info); err != nil {
		return nil, fmt.Errorf("ошибка получения информации из таблиц: %w", err)
	}
	return info, nil
}

// getBaseDeviceInfo собирает базовую информацию: модель ККТ, версия драйвера и прошивки.
func (d *comDriver) getBaseDeviceInfo(info *FiscalInfo) error {
	var major, minor, release, build int32
	major, _ = d.getPropertyInt32("DriverMajorVersion")
	minor, _ = d.getPropertyInt32("DriverMinorVersion")
	release, _ = d.getPropertyInt32("DriverRelease")
	build, _ = d.getPropertyInt32("DriverBuild")
	info.InstalledDriver = fmt.Sprintf("%d.%d.%d.%d", major, minor, release, build)

	oleutil.CallMethod(d.dispatch, "GetDeviceMetrics")
	if err := d.checkError(); err != nil {
		return err
	}
	info.ModelName, _ = d.getPropertyString("UDescription")

	oleutil.CallMethod(d.dispatch, "GetECRStatus")
	if err := d.checkError(); err != nil {
		return err
	}
	ecrSoftDateVar, err := d.getPropertyVariant("ECRSoftDate")
	if err == nil {
		defer ecrSoftDateVar.Clear()
		if ecrSoftDate, ok := ecrSoftDateVar.Value().(time.Time); ok && !ecrSoftDate.IsZero() {
			info.SoftwareDate = ecrSoftDate.Format("2006-01-02")
		}
	}

	// Получаем и расшифровываем информацию о лицензии
	if _, err := oleutil.CallMethod(d.dispatch, "ReadFeatureLicenses"); err == nil {
		if errCheck := d.checkError(); errCheck == nil {
			hexLicense, _ := d.getPropertyString("License")
			info.SubscriptionInfo = decodeLicense(hexLicense)
			if info.SubscriptionInfo != "" {
				log.Printf("Информация о лицензии успешно расшифрована: %s", info.SubscriptionInfo)
			} else if hexLicense != "" {
				log.Printf("Не удалось распознать формат полученной лицензии: %s", hexLicense)
			}
		}
	} else {
		log.Printf("Предупреждение: команда ReadFeatureLicenses не выполнена, информация о лицензиях недоступна.")
	}
	return nil
}

// getFiscalizationInfo получает данные из последнего документа о регистрации/перерегистрации.
func (d *comDriver) getFiscalizationInfo(info *FiscalInfo) error {
	log.Println("Запрос данных последней фискализации (FNGetFiscalizationResult)...")
	oleutil.PutProperty(d.dispatch, "RegistrationNumber", 1) // Запрашиваем первый (последний) документ
	if _, err := oleutil.CallMethod(d.dispatch, "FNGetFiscalizationResult"); err != nil {
		return err
	}
	if err := d.checkError(); err != nil {
		return err
	}

	info.RNM, _ = d.getPropertyString("KKTRegistrationNumber")
	inn, _ := d.getPropertyString("INN")
	info.Inn = strings.TrimSpace(inn)

	regDateVar, err := d.getPropertyVariant("Date")
	if err == nil {
		defer regDateVar.Clear()
		regTimeStr, _ := d.getPropertyString("Time")
		if regDateOle, ok := regDateVar.Value().(time.Time); ok {
			regTime, _ := time.Parse("15:04:05", regTimeStr)
			info.RegistrationDate = time.Date(regDateOle.Year(), regDateOle.Month(), regDateOle.Day(), regTime.Hour(), regTime.Minute(), regTime.Second(), 0, time.Local).Format("2006-01-02 15:04:05")
		}
	}

	workMode, _ := d.getPropertyInt32("WorkMode")
	workModeEx, _ := d.getPropertyInt32("WorkModeEx")
	info.AttributeMarked = (workMode & 0x10) != 0   // Бит 4 - признак торговли маркированными товарами
	info.AttributeExcise = (workModeEx & 0x01) != 0 // Бит 0 - признак торговли подакцизными товарами
	return nil
}

// getFnInfo собирает информацию непосредственно с фискального накопителя.
func (d *comDriver) getFnInfo(info *FiscalInfo) error {
	log.Println("Запрос данных ФН...")
	oleutil.CallMethod(d.dispatch, "FNGetSerial")
	if err := d.checkError(); err != nil {
		return err
	}
	info.FnSerial, _ = d.getPropertyString("SerialNumber")

	oleutil.CallMethod(d.dispatch, "FNGetExpirationTime")
	if err := d.checkError(); err != nil {
		return err
	}
	fnEndDateVar, err := d.getPropertyVariant("Date")
	if err == nil {
		defer fnEndDateVar.Clear()
		if fnEndDate, ok := fnEndDateVar.Value().(time.Time); ok {
			info.FnEndDate = fnEndDate.Format("2006-01-02 15:04:05")
		}
	}

	oleutil.CallMethod(d.dispatch, "FNGetImplementation")
	if err := d.checkError(); err != nil {
		return err
	}
	fnExec, _ := d.getPropertyString("FNImplementation")
	info.FnExecution = strings.TrimSpace(fnExec)
	return nil
}

// getInfoFromTables читает данные из внутренних таблиц ККТ,
// которые недоступны через высокоуровневые методы.
func (d *comDriver) getInfoFromTables(info *FiscalInfo) error {
	log.Println("Чтение данных из таблиц ККТ...")
	sn, err := d.readTableField(18, 1, 1)
	if err == nil {
		info.SerialNumber = strings.TrimSpace(sn)
	}
	orgName, err := d.readTableField(18, 1, 7)
	if err == nil {
		info.OrganizationName = strings.TrimSpace(orgName)
	}
	ofdName, err := d.readTableField(18, 1, 10)
	if err == nil {
		info.OfdName = strings.TrimSpace(ofdName)
	}
	address, err := d.readTableField(18, 1, 9)
	if err == nil {
		info.Address = strings.TrimSpace(address)
	}

	// Версия ФФД хранится в виде кода: 2 - "1.05", 4 - "1.2"
	ffdValueStr, err := d.readTableField(17, 1, 17)
	if err != nil {
		info.FfdVersion = "не определена"
	} else {
		ffdValue, _ := strconv.Atoi(strings.TrimSpace(ffdValueStr))
		switch ffdValue {
		case 2:
			info.FfdVersion = "105"
		case 4:
			info.FfdVersion = "120"
		default:
			info.FfdVersion = fmt.Sprintf("неизвестный код (%d)", ffdValue)
		}
	}
	return nil
}

// readTableField является оберткой для чтения одного поля из таблицы ККТ.
func (d *comDriver) readTableField(tableNum, rowNum, fieldNum int) (string, error) {
	oleutil.PutProperty(d.dispatch, "TableNumber", tableNum)
	oleutil.PutProperty(d.dispatch, "RowNumber", rowNum)
	oleutil.PutProperty(d.dispatch, "FieldNumber", fieldNum)
	if _, err := oleutil.CallMethod(d.dispatch, "ReadTable"); err != nil {
		return "", err
	}
	if err := d.checkError(); err != nil {
		return "", err
	}
	return d.getPropertyString("ValueOfFieldString")
}

// checkError проверяет свойство ResultCode драйвера и, если оно не равно 0,
// возвращает ошибку с текстовым описанием.
func (d *comDriver) checkError() error {
	resultCode, err := d.getPropertyInt32("ResultCode")
	if err != nil {
		return fmt.Errorf("не удалось прочитать ResultCode: %w", err)
	}
	if resultCode != 0 {
		description, _ := d.getPropertyString("ResultCodeDescription")
		return fmt.Errorf("ошибка драйвера: [%d] %s", resultCode, description)
	}
	return nil
}

// getPropertyVariant - низкоуровневый хелпер для получения свойства в виде OLE VARIANT.
func (d *comDriver) getPropertyVariant(propName string) (*ole.VARIANT, error) {
	return oleutil.GetProperty(d.dispatch, propName)
}

// getPropertyString получает свойство и преобразует его в строку.
func (d *comDriver) getPropertyString(propName string) (string, error) {
	variant, err := d.getPropertyVariant(propName)
	if err != nil {
		return "", fmt.Errorf("не удалось получить свойство '%s': %w", propName, err)
	}
	defer variant.Clear()
	return variant.ToString(), nil
}

// getPropertyInt32 получает свойство и пытается преобразовать его в int32.
// Обрабатывает различные числовые типы, которые может вернуть COM-объект.
func (d *comDriver) getPropertyInt32(propName string) (int32, error) {
	variant, err := d.getPropertyVariant(propName)
	if err != nil {
		return 0, fmt.Errorf("не удалось получить свойство '%s': %w", propName, err)
	}
	defer variant.Clear()
	v := variant.Value()
	if v == nil {
		return 0, nil
	}
	switch val := v.(type) {
	case int:
		return int32(val), nil
	case int8:
		return int32(val), nil
	case int16:
		return int32(val), nil
	case int32:
		return val, nil
	case int64:
		return int32(val), nil
	case uint:
		return int32(val), nil
	case uint8:
		return int32(val), nil
	case uint16:
		return int32(val), nil
	case uint32:
		return int32(val), nil
	case uint64:
		return int32(val), nil
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(val), 10, 32)
		if err == nil {
			return int32(i), nil
		}
		return 0, fmt.Errorf("не удалось сконвертировать строку '%s' в int32 для свойства %s", val, propName)
	default:
		return 0, fmt.Errorf("неожиданный тип для %s: %T", propName, v)
	}
}

// SearchDevices выполняет двухэтапный поиск ККТ: сначала на COM-портах,
// затем в стандартных для RNDIS IP-подсетях.
func SearchDevices(comTimeout, tcpTimeout time.Duration) ([]Config, error) {
	var foundDevices []Config

	// Этап 1: Последовательный поиск на COM-портах.
	log.Println("--- Начинаю поиск устройств на COM-портах ---")
	ports, err := serial.GetPortsList()
	if err != nil {
		log.Printf("Не удалось получить список COM-портов: %v", err)
	} else if len(ports) == 0 {
		log.Println("В системе не найдено COM-портов.")
	} else {
		log.Printf("Найдены COM-порты: %v. Начинаю проверку...", ports)
		for _, portName := range ports {
			log.Printf("Проверяю порт %s...", portName)
			config, err := findOnComPort(portName, comTimeout)
			if err == nil {
				foundDevices = append(foundDevices, *config)
			}
		}
	}

	// Этап 2: Параллельный поиск в RNDIS-сетях.
	log.Println("--- Начинаю поиск устройств в RNDIS-сетях ---")
	var wg sync.WaitGroup
	foundChan := make(chan Config)

	wg.Add(1)
	go func() {
		defer wg.Done()
		scanRNDISNetworks(tcpTimeout, foundChan)
	}()

	go func() {
		wg.Wait()
		close(foundChan)
	}()

	for config := range foundChan {
		foundDevices = append(foundDevices, config)
	}

	log.Printf("--- Поиск завершен. Всего найдено устройств: %d ---", len(foundDevices))
	return foundDevices, nil
}

// findOnComPort проверяет один COM-порт на наличие ККТ, перебирая
// ограниченный набор скоростей для ускорения процесса.
func findOnComPort(portName string, timeout time.Duration) (*Config, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	comNum, err := strconv.Atoi(strings.TrimPrefix(strings.ToUpper(portName), "COM"))
	if err != nil {
		return nil, fmt.Errorf("некорректное имя порта: %s", portName)
	}

	// Ограниченный список скоростей для быстрой проверки.
	baudRates := []int32{115200, 4800}
	// Индексы скоростей, которые понимает драйвер.
	baudRateIndexes := map[int32]int32{
		115200: 6,
		4800:   1,
	}

	for _, baud := range baudRates {
		// Для каждой попытки на каждой скорости требуется полный цикл
		// инициализации и деинициализации COM.
		if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
			if err := ole.CoInitialize(0); err != nil {
				continue
			}
		}

		unknown, err := oleutil.CreateObject("AddIn.DrvFR")
		if err != nil {
			ole.CoUninitialize()
			continue
		}
		dispatch, err := unknown.QueryInterface(ole.IID_IDispatch)
		if err != nil {
			unknown.Release()
			ole.CoUninitialize()
			continue
		}

		// Настройка параметров для быстрой проверки с таймаутом.
		oleutil.PutProperty(dispatch, "ConnectionType", 0)
		oleutil.PutProperty(dispatch, "Password", 30)
		oleutil.PutProperty(dispatch, "ComNumber", comNum)
		oleutil.PutProperty(dispatch, "BaudRate", baudRateIndexes[baud])
		oleutil.PutProperty(dispatch, "Timeout", timeout.Milliseconds())

		// Попытка подключения и проверка кода ошибки драйвера.
		_, connectErr := oleutil.CallMethod(dispatch, "Connect")
		tempDriver := &comDriver{dispatch: dispatch}
		checkErr := tempDriver.checkError()

		// Освобождение ресурсов перед следующей итерацией или выходом.
		dispatch.Release()
		unknown.Release()
		ole.CoUninitialize()

		if connectErr == nil && checkErr == nil {
			// Успех, устройство найдено.
			log.Printf("!!! Устройство найдено на порту %s, скорость %d", portName, baud)
			return &Config{
				ConnectionType: 0,
				ComName:        portName,
				ComNumber:      int32(comNum),
				BaudRate:       baudRateIndexes[baud],
				Password:       30,
			}, nil
		}
	}
	return nil, fmt.Errorf("устройство не найдено на порту %s", portName)
}

// scanRNDISNetworks запускает параллельное сканирование стандартных подсетей
// для RNDIS-устройств. Использует пул горутин для ограничения нагрузки.
func scanRNDISNetworks(timeout time.Duration, foundChan chan<- Config) {
	var wg sync.WaitGroup
	ports := []int32{7778} // Стандартный порт для Штрих-М.
	subnets := []string{"192.168.137.", "192.168.138."}

	// Ограничиваем количество одновременных горутин.
	const maxGoroutines = 50
	guard := make(chan struct{}, maxGoroutines)

	for _, subnet := range subnets {
		for i := 1; i <= 254; i++ {
			ip := subnet + strconv.Itoa(i)
			wg.Add(1)
			guard <- struct{}{} // Занимаем слот в пуле.
			go func(ip string, port int32) {
				defer wg.Done()
				checkIP(ip, port, timeout, foundChan)
				<-guard // Освобождаем слот.
			}(ip, ports[0])
		}
	}
	wg.Wait()
}

// checkIP выполняет двухэтапную проверку одного IP-адреса:
// 1. Быстрая проверка доступности порта через net.DialTimeout.
// 2. Полное подключение через драйвер для верификации, что это ККТ.
func checkIP(ip string, port int32, timeout time.Duration, foundChan chan<- Config) {
	address := fmt.Sprintf("%s:%d", ip, port)
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return // Порт закрыт или хост недоступен.
	}
	conn.Close()

	log.Printf("Найден открытый порт на %s. Проверяю совместимость...", address)
	config := Config{
		ConnectionType: 6,
		IPAddress:      ip,
		TCPPort:        port,
		Password:       30,
	}
	driver := New(config)
	if err := driver.Connect(); err == nil {
		log.Printf("!!! Найдено и подтверждено устройство по TCP/IP: %s", address)
		foundChan <- config
		driver.Disconnect()
	}
}
