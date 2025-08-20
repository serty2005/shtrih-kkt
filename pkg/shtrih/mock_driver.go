package shtrih

// mockDriver — это имитация драйвера для тестирования.
type mockDriver struct {
	FiscalInfoToReturn *FiscalInfo
	ErrorToReturn      error
}

// NewMock создает новый мок-драйвер.
func NewMock(info *FiscalInfo, err error) Driver {
	return &mockDriver{
		FiscalInfoToReturn: info,
		ErrorToReturn:      err,
	}
}

func (m *mockDriver) Connect() error {
	if m.ErrorToReturn != nil {
		return m.ErrorToReturn
	}
	return nil
}

func (m *mockDriver) Disconnect() error {
	return nil
}

func (m *mockDriver) GetFiscalInfo() (*FiscalInfo, error) {
	if m.ErrorToReturn != nil {
		return nil, m.ErrorToReturn
	}
	return m.FiscalInfoToReturn, nil
}
