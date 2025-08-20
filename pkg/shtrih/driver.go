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

// ... (структуры, интерфейс, New, Connect, Disconnect и все остальные методы остаются без изменений) ...
type Config struct {
	ConnectionType int32  `json:"connectionType"`
	IPAddress      string `json:"ipAddress,omitempty"`
	TCPPort        int32  `json:"tcpPort,omitempty"`
	ComName        string `json:"comName,omitempty"`
	ComNumber      int32  `json:"-"`
	BaudRate       int32  `json:"baudRate,omitempty"`
	Password       int32  `json:"-"`
}
type FiscalInfo struct {
	ModelName        string `json:"modelName"`
	SerialNumber     string `json:"serialNumber"`
	RNM              string `json:"RNM"`
	OrganizationName string `json:"organizationName"`
	Inn              string `json:"INN"`
	FnSerial         string `json:"fn_serial"`
	RegistrationDate string `json:"datetime_reg"`
	FnEndDate        string `json:"dateTime_end"`
	OfdName          string `json:"ofdName"`
	SoftwareDate     string `json:"bootVersion"`
	FfdVersion       string `json:"ffdVersion"`
	FnExecution      string `json:"fnExecution"`
	InstalledDriver  string `json:"installed_driver"`
	AttributeExcise  bool   `json:"attribute_excise"`
	AttributeMarked  bool   `json:"attribute_marked"`
	LicensesRawHex   string `json:"licenses,omitempty"`
}
type Driver interface {
	Connect() error
	Disconnect() error
	GetFiscalInfo() (*FiscalInfo, error)
}
type comDriver struct {
	config    Config
	dispatch  *ole.IDispatch
	connected bool
}

func New(config Config) Driver { return &comDriver{config: config} }
func (d *comDriver) Connect() error {
	if d.connected {
		return nil
	}
	runtime.LockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		if err := ole.CoInitialize(0); err != nil {
			runtime.UnlockOSThread()
			return fmt.Errorf("COM init failed: %w", err)
		}
	}
	unknown, err := oleutil.CreateObject("AddIn.DrvFR")
	if err != nil {
		ole.CoUninitialize()
		runtime.UnlockOSThread()
		return fmt.Errorf("create COM object failed: %w", err)
	}
	d.dispatch, err = unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		unknown.Release()
		ole.CoUninitialize()
		runtime.UnlockOSThread()
		return fmt.Errorf("query interface failed: %w", err)
	}
	unknown.Release()
	oleutil.PutProperty(d.dispatch, "ConnectionType", d.config.ConnectionType)
	oleutil.PutProperty(d.dispatch, "Password", d.config.Password)
	if d.config.ConnectionType == 0 {
		oleutil.PutProperty(d.dispatch, "ComNumber", d.config.ComNumber)
		oleutil.PutProperty(d.dispatch, "BaudRate", d.config.BaudRate)
	} else if d.config.ConnectionType == 6 {
		oleutil.PutProperty(d.dispatch, "IPAddress", d.config.IPAddress)
		oleutil.PutProperty(d.dispatch, "TCPPort", d.config.TCPPort)
		oleutil.PutProperty(d.dispatch, "UseIPAddress", true)
	}
	if _, err := oleutil.CallMethod(d.dispatch, "Connect"); err != nil {
		d.Disconnect()
		return fmt.Errorf("connect call failed: %w", err)
	}
	if err := d.checkError(); err != nil {
		d.Disconnect()
		return fmt.Errorf("driver error on connect: %w", err)
	}
	d.connected = true
	log.Println("Подключение к ККТ успешно установлено.")
	return nil
}
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
func (d *comDriver) getBaseDeviceInfo(info *FiscalInfo) error {
	var err error
	var major, minor, release, build int32
	major, err = d.getPropertyInt32("DriverMajorVersion")
	if err != nil {
		return err
	}
	minor, err = d.getPropertyInt32("DriverMinorVersion")
	if err != nil {
		return err
	}
	release, err = d.getPropertyInt32("DriverRelease")
	if err != nil {
		return err
	}
	build, err = d.getPropertyInt32("DriverBuild")
	if err != nil {
		return err
	}
	info.InstalledDriver = fmt.Sprintf("%d.%d.%d.%d", major, minor, release, build)
	if _, err = oleutil.CallMethod(d.dispatch, "GetDeviceMetrics"); err != nil {
		return err
	}
	if err = d.checkError(); err != nil {
		return err
	}
	info.ModelName, err = d.getPropertyString("UDescription")
	if err != nil {
		return err
	}
	if _, err = oleutil.CallMethod(d.dispatch, "GetECRStatus"); err != nil {
		return err
	}
	if err = d.checkError(); err != nil {
		return err
	}
	ecrSoftDateVar, err := d.getPropertyVariant("ECRSoftDate")
	if err != nil {
		return err
	}
	defer ecrSoftDateVar.Clear()
	if ecrSoftDate, ok := ecrSoftDateVar.Value().(time.Time); ok && !ecrSoftDate.IsZero() {
		info.SoftwareDate = ecrSoftDate.Format("2006-01-02")
	}
	if _, err := oleutil.CallMethod(d.dispatch, "ReadFeatureLicenses"); err == nil {
		if errCheck := d.checkError(); errCheck == nil {
			info.LicensesRawHex, _ = d.getPropertyString("License")
		} else {
			log.Printf("Предупреждение: не удалось проверить результат ReadFeatureLicenses: %v", errCheck)
		}
	} else {
		log.Printf("Предупреждение: не удалось вызвать метод ReadFeatureLicenses: %v", err)
	}
	return nil
}
func (d *comDriver) getFiscalizationInfo(info *FiscalInfo) error {
	log.Println("Запрос данных последней фискализации (FNGetFiscalizationResult)...")
	oleutil.PutProperty(d.dispatch, "RegistrationNumber", 1)
	if _, err := oleutil.CallMethod(d.dispatch, "FNGetFiscalizationResult"); err != nil {
		return err
	}
	if err := d.checkError(); err != nil {
		return err
	}
	var err error
	info.RNM, err = d.getPropertyString("KKTRegistrationNumber")
	if err != nil {
		return err
	}
	inn, err := d.getPropertyString("INN")
	if err != nil {
		return err
	}
	info.Inn = strings.TrimSpace(inn)
	regDateVar, err := d.getPropertyVariant("Date")
	if err != nil {
		return err
	}
	defer regDateVar.Clear()
	regTimeStr, err := d.getPropertyString("Time")
	if err != nil {
		return err
	}
	if regDateOle, ok := regDateVar.Value().(time.Time); ok {
		regTime, _ := time.Parse("15:04:05", regTimeStr)
		info.RegistrationDate = time.Date(regDateOle.Year(), regDateOle.Month(), regDateOle.Day(), regTime.Hour(), regTime.Minute(), regTime.Second(), 0, time.Local).Format("2006-01-02 15:04:05")
	}
	workMode, err := d.getPropertyInt32("WorkMode")
	if err != nil {
		return err
	}
	workModeEx, err := d.getPropertyInt32("WorkModeEx")
	if err != nil {
		return err
	}
	info.AttributeMarked = (workMode & 0x10) != 0
	info.AttributeExcise = (workModeEx & 0x01) != 0
	return nil
}
func (d *comDriver) getFnInfo(info *FiscalInfo) error {
	log.Println("Запрос данных ФН...")
	var err error
	if _, err = oleutil.CallMethod(d.dispatch, "FNGetSerial"); err != nil {
		return err
	}
	if err = d.checkError(); err != nil {
		return err
	}
	info.FnSerial, err = d.getPropertyString("SerialNumber")
	if err != nil {
		return err
	}
	if _, err = oleutil.CallMethod(d.dispatch, "FNGetExpirationTime"); err != nil {
		return err
	}
	if err = d.checkError(); err != nil {
		return err
	}
	fnEndDateVar, err := d.getPropertyVariant("Date")
	if err != nil {
		return err
	}
	defer fnEndDateVar.Clear()
	if fnEndDate, ok := fnEndDateVar.Value().(time.Time); ok {
		info.FnEndDate = fnEndDate.Format("2006-01-02 15:04:05")
	}
	if _, err = oleutil.CallMethod(d.dispatch, "FNGetImplementation"); err != nil {
		return err
	}
	if err = d.checkError(); err != nil {
		return err
	}
	fnExec, err := d.getPropertyString("FNImplementation")
	if err != nil {
		return err
	}
	info.FnExecution = strings.TrimSpace(fnExec)
	return nil
}
func (d *comDriver) getInfoFromTables(info *FiscalInfo) error {
	log.Println("Чтение данных из таблиц ККТ...")
	sn, err := d.readTableField(18, 1, 1)
	if err != nil {
		log.Printf("Предупреждение: не удалось прочитать серийный номер из таблицы 18, поля 1: %v", err)
	} else {
		info.SerialNumber = strings.TrimSpace(sn)
	}
	orgName, err := d.readTableField(18, 1, 7)
	if err != nil {
		log.Printf("Предупреждение: не удалось прочитать название организации из таблицы 18, поля 7: %v", err)
	} else {
		info.OrganizationName = strings.TrimSpace(orgName)
	}
	ofdName, err := d.readTableField(18, 1, 10)
	if err != nil {
		log.Printf("Предупреждение: не удалось прочитать название ОФД из таблицы 18, поля 10: %v", err)
	} else {
		info.OfdName = strings.TrimSpace(ofdName)
	}
	ffdValueStr, err := d.readTableField(17, 1, 17)
	if err != nil {
		log.Printf("Предупреждение: не удалось прочитать версию ФФД из таблицы 18, поля 17: %v", err)
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
func (d *comDriver) getPropertyVariant(propName string) (*ole.VARIANT, error) {
	return oleutil.GetProperty(d.dispatch, propName)
}
func (d *comDriver) getPropertyString(propName string) (string, error) {
	variant, err := d.getPropertyVariant(propName)
	if err != nil {
		return "", fmt.Errorf("не удалось получить свойство '%s': %w", propName, err)
	}
	defer variant.Clear()
	return variant.ToString(), nil
}
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

// --- НОВЫЙ И ИЗМЕНЕННЫЙ КОД ---

// SearchDevices выполняет последовательный поиск ККТ на COM-портах,
// а затем параллельный поиск в стандартных RNDIS IP-подсетях.
func SearchDevices(comTimeout, tcpTimeout time.Duration) ([]Config, error) {
	log.Println("--- Начинаю поиск устройств на COM-портах ---")
	var foundDevices []Config

	// --- Этап 1: Последовательный поиск на COM-портах ---
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
				log.Printf("!!! Устройство найдено на порту %s, скорость: %d", config.ComName, config.BaudRate)
				foundDevices = append(foundDevices, *config)
			}
		}
	}

	// --- Этап 2: Параллельный поиск в RNDIS-сетях ---
	log.Println("--- Начинаю поиск устройств в RNDIS-сетях ---")

	var wg sync.WaitGroup
	foundChan := make(chan Config)

	// Запускаем сканирование в отдельной горутине
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanRNDISNetworks(tcpTimeout, foundChan)
	}()

	// Горутина для закрытия канала после завершения сканирования
	go func() {
		wg.Wait()
		close(foundChan)
	}()

	// Собираем результаты из канала
	for config := range foundChan {
		foundDevices = append(foundDevices, config)
	}

	log.Printf("--- Поиск завершен. Всего найдено устройств: %d ---", len(foundDevices))
	return foundDevices, nil
}

// findOnComPort инкапсулирует весь жизненный цикл COM для поиска на одном порту.
// Он включает дополнительную верификацию через быстрый запрос состояния.
func findOnComPort(portName string, timeout time.Duration) (*Config, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	comNum, err := strconv.Atoi(strings.TrimPrefix(strings.ToUpper(portName), "COM"))
	if err != nil {
		return nil, fmt.Errorf("некорректное имя порта: %s", portName)
	}

	// Стандартные скорости обмена для ККТ
	baudRates := []int32{115200, 4800}
	// В документации BaudRate - это индекс от 0 до 6.
	// 6 = 115200, 5 = 57600, 4 = 38400, 3 = 19200, 2 = 9600
	baudRateIndexes := map[int32]int32{
		115200: 6,
		4800:   1,
	}

	for _, baud := range baudRates {
		// Для каждой скорости выполняем полную проверку
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

		// Настраиваем драйвер для быстрого подключения
		oleutil.PutProperty(dispatch, "ConnectionType", 0)
		oleutil.PutProperty(dispatch, "Password", 30)
		oleutil.PutProperty(dispatch, "ComNumber", comNum)
		oleutil.PutProperty(dispatch, "BaudRate", baudRateIndexes[baud])
		oleutil.PutProperty(dispatch, "Timeout", timeout.Milliseconds()) // Устанавливаем короткий таймаут!

		// Пытаемся подключиться
		_, connectErr := oleutil.CallMethod(dispatch, "Connect")
		tempDriver := &comDriver{dispatch: dispatch}
		checkErr := tempDriver.checkError()

		if connectErr == nil && checkErr == nil {
			// Успех!
			log.Printf("Успешная верификация на порту %s, скорость %d", portName, baud)
			dispatch.Release()
			unknown.Release()
			ole.CoUninitialize()
			return &Config{
				ConnectionType: 0,
				ComName:        portName,
				ComNumber:      int32(comNum),
				BaudRate:       baudRateIndexes[baud],
				Password:       30,
			}, nil
		}

		// Неудача, освобождаем ресурсы и пробуем следующую скорость
		dispatch.Release()
		unknown.Release()
		ole.CoUninitialize()
	}

	return nil, fmt.Errorf("устройство не найдено на порту %s", portName)
}

// scanRNDISNetworks ищет устройства в стандартных RNDIS-подсетях.
func scanRNDISNetworks(timeout time.Duration, foundChan chan<- Config) {
	var wg sync.WaitGroup
	ports := []int32{7778} // Стандартный порт для Штрих-М
	subnets := []string{"192.168.137.", "192.168.138."}

	// Ограничиваем количество одновременных горутин, чтобы не перегружать систему
	const maxGoroutines = 50
	guard := make(chan struct{}, maxGoroutines)

	for _, subnet := range subnets {
		for i := 1; i <= 254; i++ {
			ip := subnet + strconv.Itoa(i)

			wg.Add(1)
			guard <- struct{}{} // Занимаем слот
			go func(ip string, port int32) {
				defer wg.Done()
				checkIP(ip, port, timeout, foundChan)
				<-guard // Освобождаем слот
			}(ip, ports[0])
		}
	}
	wg.Wait()
}

// checkIP проверяет доступность порта и верифицирует, что на нем находится ККТ.
func checkIP(ip string, port int32, timeout time.Duration, foundChan chan<- Config) {
	address := fmt.Sprintf("%s:%d", ip, port)
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return // Порт закрыт или хост недоступен
	}
	conn.Close()

	// Порт открыт, теперь проверяем, действительно ли это наша ККТ
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
	} else {
		log.Printf("Порт %s открыт, но устройство не ответило как ККТ: %v", address, err)
	}
}
