// Package shtrih (продолжение)
package shtrih

import (
	"fmt"
	"log"
)

// mockDriver представляет собой имитацию реального драйвера для целей тестирования.
// Он реализует интерфейс Driver.
type mockDriver struct {
	// MockData - это структура с фискальными данными, которую вернет GetFiscalInfo.
	MockData *FiscalInfo
	// ConnectErr - ошибка, которую вернет метод Connect, если она задана.
	ConnectErr error
	// GetFiscalInfoErr - ошибка, которую вернет метод GetFiscalInfo, если она задана.
	GetFiscalInfoErr error

	// Внутренние флаги для проверки вызовов в тестах.
	connected           bool
	ConnectCalled       bool
	DisconnectCalled    bool
	GetFiscalInfoCalled bool
}

// NewMockDriver является конструктором для создания нового мок-драйвера.
// Позволяет заранее определить, какие данные и ошибки будут возвращаться.
func NewMockDriver(data *FiscalInfo, connectErr, getInfoErr error) Driver {
	return &mockDriver{
		MockData:         data,
		ConnectErr:       connectErr,
		GetFiscalInfoErr: getInfoErr,
	}
}

// Connect имитирует подключение к ККТ.
func (m *mockDriver) Connect() error {
	m.ConnectCalled = true
	log.Println("Mock Driver: Connect() вызван.")

	// Если была задана ошибка подключения, возвращаем ее.
	if m.ConnectErr != nil {
		return m.ConnectErr
	}
	// Если уже "подключены", ничего не делаем.
	if m.connected {
		return nil
	}
	m.connected = true
	return nil
}

// Disconnect имитирует отключение от ККТ.
func (m *mockDriver) Disconnect() error {
	m.DisconnectCalled = true
	log.Println("Mock Driver: Disconnect() вызван.")

	// Если не были "подключены", ничего не делаем.
	if !m.connected {
		return nil
	}
	m.connected = false
	return nil
}

// GetFiscalInfo имитирует получение фискальных данных.
func (m *mockDriver) GetFiscalInfo() (*FiscalInfo, error) {
	m.GetFiscalInfoCalled = true
	log.Println("Mock Driver: GetFiscalInfo() вызван.")

	// Проверяем, было ли установлено "соединение".
	if !m.connected {
		return nil, fmt.Errorf("мок-драйвер: не подключен")
	}
	// Если была задана ошибка получения данных, возвращаем ее.
	if m.GetFiscalInfoErr != nil {
		return nil, m.GetFiscalInfoErr
	}
	// Если не были предоставлены мок-данные, возвращаем ошибку.
	if m.MockData == nil {
		return nil, fmt.Errorf("мок-драйвер: данные для имитации не предоставлены")
	}

	return m.MockData, nil
}
