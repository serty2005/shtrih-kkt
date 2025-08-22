package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"shtrih-kkt/pkg/shtrih"
	"testing"
)

// loadCanonicalKKTData загружает эталонные данные ККТ из файла для тестов.
// Путь к файлу указывается относительно корня проекта.
func loadCanonicalKKTData(t *testing.T, path string) *shtrih.FiscalInfo {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Критическая ошибка: не удалось прочитать канонический файл данных '%s': %v", path, err)
	}
	var info shtrih.FiscalInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("Критическая ошибка: не удалось распарсить JSON из канонического файла '%s': %v", path, err)
	}
	return &info
}

// TestProcessDevices_NewFileFromDonor проверяет сценарий, когда для ККТ еще нет
// файла, но есть файл-донор с данными о рабочей станции.
func TestProcessDevices_NewFileFromDonor(t *testing.T) {
	// --- Arrange (Подготовка) ---

	// 1. Создаем временную директорию для теста, чтобы не засорять реальную папку `date`.
	tempDir := t.TempDir()
	originalOutputDir := outputDir
	outputDir = tempDir
	defer func() { outputDir = originalOutputDir }()

	// 2. Загружаем канонические данные для мок-драйвера из файла.
	// Этот файл должен быть создан заранее и помещен в pkg/shtrih/testdata/
	mockKKTData := loadCanonicalKKTData(t, "pkg/shtrih/testdata/canonical_kkt_data.json")

	// 3. Создаем файл-донор с уникальными данными о рабочей станции во временной папке.
	donorData := map[string]interface{}{
		"hostname":      "DONOR-PC",
		"teamviewer_id": "999888777",
		"vc":            "3.0-donor-test",
	}
	donorBytes, _ := json.Marshal(donorData)
	donorFilePath := filepath.Join(tempDir, "donor.json")
	if err := os.WriteFile(donorFilePath, donorBytes, 0644); err != nil {
		t.Fatalf("Не удалось создать тестовый файл-донор: %v", err)
	}

	// 4. Создаем "фабрику", которая вернет мок-драйвер с нашими каноническими данными.
	mockDriverFactory := func(config shtrih.Config) shtrih.Driver {
		return shtrih.NewMockDriver(mockKKTData, nil, nil)
	}

	// 5. Готовим входные данные для processDevices.
	testConfigs := []shtrih.Config{{ConnectionType: 6, IPAddress: "127.0.0.1"}}

	// --- Act (Действие) ---

	processDevices(testConfigs, mockDriverFactory)

	// --- Assert (Проверка) ---

	// 1. Проверяем, что был создан правильный JSON-файл.
	expectedFileName := mockKKTData.SerialNumber + ".json"
	resultFilePath := filepath.Join(tempDir, expectedFileName)
	if _, err := os.Stat(resultFilePath); os.IsNotExist(err) {
		t.Fatalf("Ожидалось, что будет создан файл '%s', но он не найден.", resultFilePath)
	}

	// 2. Читаем созданный файл и проверяем его содержимое.
	resultBytes, err := os.ReadFile(resultFilePath)
	if err != nil {
		t.Fatalf("Не удалось прочитать результирующий файл '%s': %v", resultFilePath, err)
	}

	var resultMap map[string]interface{}
	if err := json.Unmarshal(resultBytes, &resultMap); err != nil {
		t.Fatalf("Не удалось распарсить JSON из результирующего файла: %v", err)
	}

	// 3. Проверяем, что данные из ККТ и донора корректно слились.
	// Проверка поля от ККТ.
	if resultMap["modelName"] != mockKKTData.ModelName {
		t.Errorf("Поле 'modelName' неверно. Ожидалось '%s', получено '%v'", mockKKTData.ModelName, resultMap["modelName"])
	}

	// Проверка полей от донора.
	if resultMap["hostname"] != donorData["hostname"] {
		t.Errorf("Поле 'hostname' из донора не было добавлено. Ожидалось '%s', получено '%v'", donorData["hostname"], resultMap["hostname"])
	}
	if resultMap["vc"] != donorData["vc"] {
		t.Errorf("Поле 'vc' из донора не было добавлено. Ожидалось '%s', получено '%v'", donorData["vc"], resultMap["vc"])
	}

	// Проверка автоматически сгенерированных полей времени.
	if _, ok := resultMap["current_time"]; !ok {
		t.Error("Отсутствует обязательное поле 'current_time'.")
	}
	if _, ok := resultMap["v_time"]; !ok {
		t.Error("Отсутствует обязательное поле 'v_time'.")
	}
}

// TestFindSourceWorkstationData_FileHandling проверяет непосредственно логику
// поиска и чтения донор-файла в смоделированной файловой структуре.
func TestFindSourceWorkstationData_FileHandling(t *testing.T) {

	// --- Сценарий 1: В папке /date есть правильный донор-файл ---
	t.Run("when valid donor file exists", func(t *testing.T) {
		// Arrange: Готовим файловую систему
		tempDir := t.TempDir()
		originalOutputDir := outputDir
		outputDir = tempDir // Указываем, что наша папка "date" находится во временной директории
		defer func() { outputDir = originalOutputDir }()

		// Создаем донор-файл с уникальными данными для проверки
		donorData := map[string]interface{}{
			"hostname": "REAL-DONOR-PC",
			"vc":       "v_from_real_file",
		}
		donorBytes, _ := json.Marshal(donorData)
		if err := os.WriteFile(filepath.Join(tempDir, "donor_to_find.json"), donorBytes, 0644); err != nil {
			t.Fatalf("Не удалось создать тестовый файл-донор: %v", err)
		}

		// Создаем "файл-ловушку" от другого ККТ, который должен быть проигнорирован
		kktTrapData := map[string]interface{}{
			"modelName":    "SOME-OTHER-KKT",
			"serialNumber": "TRAP000001",
			"hostname":     "FAKE-HOSTNAME",
		}
		trapBytes, _ := json.Marshal(kktTrapData)
		if err := os.WriteFile(filepath.Join(tempDir, "000001.json"), trapBytes, 0644); err != nil {
			t.Fatalf("Не удалось создать файл-ловушку: %v", err)
		}

		// Act: Вызываем тестируемую функцию
		resultMap := findSourceWorkstationData()

		// Assert: Проверяем результат
		if resultMap == nil {
			t.Fatal("findSourceWorkstationData() вернула nil, хотя ожидались данные из донора.")
		}

		if resultMap["hostname"] != "REAL-DONOR-PC" {
			t.Errorf("Hostname из донора прочитан неверно. Ожидалось 'REAL-DONOR-PC', получено '%v'", resultMap["hostname"])
		}
		if resultMap["vc"] != "v_from_real_file" {
			t.Errorf("Поле 'vc' из донора прочитано неверно. Ожидалось 'v_from_real_file', получено '%v'", resultMap["vc"])
		}
	})

	// --- Сценарий 2: В папке /date нет подходящих файлов ---
	t.Run("when no valid donor file exists", func(t *testing.T) {
		// Arrange: Готовим файловую систему
		tempDir := t.TempDir()
		originalOutputDir := outputDir
		outputDir = tempDir
		defer func() { outputDir = originalOutputDir }()

		// Создаем только "файл-ловушку", который не должен считаться донором
		kktTrapData := map[string]interface{}{
			"modelName":    "SOME-OTHER-KKT",
			"serialNumber": "TRAP000001",
			"hostname":     "FAKE-HOSTNAME",
		}
		trapBytes, _ := json.Marshal(kktTrapData)
		if err := os.WriteFile(filepath.Join(tempDir, "000001.json"), trapBytes, 0644); err != nil {
			t.Fatalf("Не удалось создать файл-ловушку: %v", err)
		}
		// Создаем текстовый файл, который тоже должен быть проигнорирован
		if err := os.WriteFile(filepath.Join(tempDir, "readme.txt"), []byte("info"), 0644); err != nil {
			t.Fatalf("Не удалось создать текстовый файл: %v", err)
		}

		// Act: Вызываем тестируемую функцию
		resultMap := findSourceWorkstationData()

		// Assert: Проверяем, что функция ничего не нашла
		if resultMap != nil {
			t.Fatal("findSourceWorkstationData() вернула данные, хотя в папке не было валидных доноров.")
		}
	})
}
