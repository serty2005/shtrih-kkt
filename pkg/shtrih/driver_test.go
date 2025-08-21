// Тесты для пакета shtrih
package shtrih

import (
	"fmt"
	"reflect"
	"testing"
)

// getSampleFiscalInfo создает пример структуры FiscalInfo для использования в тестах.
func getSampleFiscalInfo() *FiscalInfo {
	return &FiscalInfo{
		ModelName:        "ШТРИХ-М-01Ф",
		SerialNumber:     "0012345678901234",
		RNM:              "0009876543210987",
		OrganizationName: "ООО Ромашка",
		Inn:              "7701234567",
		FnSerial:         "9960440300112233",
		FfdVersion:       "120",
	}
}

// TestMockDriver_SuccessfulPath проверяет стандартный успешный сценарий:
// подключение -> получение данных -> отключение.
func TestMockDriver_SuccessfulPath(t *testing.T) {
	// Arrange: Готовим данные и создаем мок-драйвер.
	sampleData := getSampleFiscalInfo()
	driver := NewMockDriver(sampleData, nil, nil)

	// Act 1: Подключаемся.
	err := driver.Connect()
	if err != nil {
		t.Fatalf("Connect() вернул неожиданную ошибку: %v", err)
	}

	// Act 2: Получаем фискальную информацию.
	info, err := driver.GetFiscalInfo()
	if err != nil {
		t.Fatalf("GetFiscalInfo() вернул неожиданную ошибку: %v", err)
	}

	// Act 3: Отключаемся.
	err = driver.Disconnect()
	if err != nil {
		t.Fatalf("Disconnect() вернул неожиданную ошибку: %v", err)
	}

	// Assert: Проверяем, что полученные данные соответствуют ожидаемым.
	if !reflect.DeepEqual(info, sampleData) {
		t.Errorf("Полученные данные не совпадают с мок-данными.\nПолучено: %+v\nОжидалось: %+v", info, sampleData)
	}

	// Assert: Проверяем, что все методы были вызваны.
	// Для этого нам нужно преобразовать интерфейс обратно в конкретный тип мок-драйвера.
	mock, ok := driver.(*mockDriver)
	if !ok {
		t.Fatal("Не удалось преобразовать драйвер в *mockDriver для проверки вызовов.")
	}

	if !mock.ConnectCalled {
		t.Error("Ожидалось, что Connect() будет вызван, но этого не произошло.")
	}
	if !mock.GetFiscalInfoCalled {
		t.Error("Ожидалось, что GetFiscalInfo() будет вызван, но этого не произошло.")
	}
	if !mock.DisconnectCalled {
		t.Error("Ожидалось, что Disconnect() будет вызван, но этого не произошло.")
	}
}

// TestMockDriver_ConnectError проверяет, что драйвер корректно обрабатывает
// ошибку, возвращаемую при подключении.
func TestMockDriver_ConnectError(t *testing.T) {
	// Arrange: Создаем симулируемую ошибку.
	simulatedError := fmt.Errorf("порт COM5 занят")
	driver := NewMockDriver(nil, simulatedError, nil)

	// Act: Пытаемся подключиться.
	err := driver.Connect()

	// Assert: Проверяем, что была возвращена именно наша ошибка.
	if err == nil {
		t.Fatal("Connect() не вернул ошибку, хотя ожидалось.")
	}
	if err != simulatedError {
		t.Errorf("Connect() вернул неверную ошибку. Получено: %v, Ожидалось: %v", err, simulatedError)
	}
}

// TestMockDriver_GetInfoWhileDisconnected проверяет, что попытка получить
// данные без предварительного подключения вернет ошибку.
func TestMockDriver_GetInfoWhileDisconnected(t *testing.T) {
	// Arrange: Создаем стандартный мок-драйвер.
	driver := NewMockDriver(getSampleFiscalInfo(), nil, nil)

	// Act: Сразу пытаемся получить данные.
	_, err := driver.GetFiscalInfo()

	// Assert: Проверяем, что получили ошибку.
	if err == nil {
		t.Fatal("GetFiscalInfo() не вернул ошибку при вызове без подключения.")
	}
}

// TestMockDriver_GetInfoError проверяет, что драйвер корректно обрабатывает
// ошибку, возвращаемую при получении данных.
func TestMockDriver_GetInfoError(t *testing.T) {
	// Arrange: Создаем симулируемую ошибку.
	simulatedError := fmt.Errorf("ошибка чтения ФН")
	driver := NewMockDriver(nil, nil, simulatedError)

	// Act
	err := driver.Connect()
	if err != nil {
		t.Fatalf("Connect() неожиданно вернул ошибку: %v", err)
	}
	info, err := driver.GetFiscalInfo()

	// Assert
	if err == nil {
		t.Fatal("GetFiscalInfo() не вернул ошибку, хотя ожидалось.")
	}
	if err != simulatedError {
		t.Errorf("GetFiscalInfo() вернул неверную ошибку. Получено: %v, Ожидалось: %v", err, simulatedError)
	}
	if info != nil {
		t.Error("GetFiscalInfo() вернул данные вместе с ошибкой, хотя должен был вернуть nil.")
	}
}
