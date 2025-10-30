// Файл: pkg/shtrih/license.go
package shtrih

import (
	"fmt"
	"sort"
	"strings"
)

// licenseInfo хранит информацию о квартале и годе для конкретного суффикса лицензии.
type licenseInfo struct {
	quarter string
	year    int
}

// licenseMap сопоставляет суффиксы HEX-лицензий с датой окончания подписки.
// Ключи должны быть в верхнем регистре.
var licenseMap = map[string]licenseInfo{
	// 2027
	"FFFFFFFF": {"4", 2027},
	"FFFFFF7F": {"3", 2027},
	"FFFFFF3F": {"2", 2027},
	"FFFFFF1F": {"1", 2027},
	// 2026
	"FFFFFF0F": {"4", 2026},
	"FFFFFF07": {"3", 2026},
	"FFFFFF03": {"2", 2026},
	"FFFFFF01": {"1", 2026},
	// 2025
	"FFFFFF00": {"4", 2025},
	"FFFF7F00": {"3", 2025},
	"FFFF3F00": {"2", 2025},
	"FFFF1F00": {"1", 2025},
	// 2024
	"FFFF0F00": {"4", 2024},
	"FFFF0700": {"3", 2024},
	"FFFF0300": {"2", 2024},
	"FFFF0100": {"1", 2024},
	// 2023
	"FFFF": {"4", 2023},
	"FF7F": {"3", 2023},
	"FF3F": {"2", 2023},
	"FF1F": {"1", 2023},
	// 2022
	"FF0F": {"4", 2022},
	"FF07": {"3", 2022},
	"FF03": {"2", 2022},
	"FF01": {"1", 2022},
	// 2021
	"FF00": {"4", 2021},
	"7F00": {"3", 2021},
	"3F00": {"2", 2021},
	"1F00": {"1", 2021},
	// 2020
	"0F00": {"4", 2020},
	"0700": {"3", 2020},
	"0300": {"2", 2020},
	"0100": {"1", 2020},
}

// sortedLicenseKeys хранит ключи из licenseMap, отсортированные по убыванию длины.
// Это необходимо, чтобы длинные суффиксы ("FFFFFFFF") проверялись раньше коротких ("FFFF").
var sortedLicenseKeys []string

func init() {
	keys := make([]string, 0, len(licenseMap))
	for k := range licenseMap {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return len(keys[i]) > len(keys[j])
	})
	sortedLicenseKeys = keys
}

// decodeLicense расшифровывает HEX-строку лицензии в человекочитаемый формат.
// ИСПРАВЛЕНА ЛОГИКА:
// Теперь читает данные о подписках строго с позиции 16 знаков от начала строки.
// Если лицензия не распознана, возвращает пустую строку.
func decodeLicense(hex string) string {
	if hex == "" {
		return ""
	}
	upperHex := strings.ToUpper(hex)

	// Читаем данные о подписке с позиции 16 знаков от начала
	if len(upperHex) >= 16 {
		// Извлекаем подстроку, начиная с 16-го знака (индекс 16)
		subscriptionHex := upperHex[16:]

		// Итерируемся по ключам, отсортированным от самого длинного к самому короткому.
		for _, licenseCode := range sortedLicenseKeys {
			// Проверяем, начинается ли подстрока с кода лицензии
			if strings.HasPrefix(subscriptionHex, licenseCode) {
				info := licenseMap[licenseCode]
				return fmt.Sprintf("Подписка до %s квартала %d года", info.quarter, info.year)
			}
		}
	}

	return ""
}
