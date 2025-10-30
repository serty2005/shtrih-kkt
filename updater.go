// Файл: updater.go
package main

import (
	"crypto"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"

	gover "github.com/hashicorp/go-version"
	"github.com/minio/selfupdate"
)

// UpdateInfo описывает структуру JSON-ответа от сервера обновлений.
// Определена только здесь.
type UpdateInfo struct {
	Version string `json:"Version"`
	Sha256  string `json:"Sha256"`
	Url     string `json:"Url"`
}

// checkForUpdates проверяет наличие новой версии и запускает процесс обновления.
func checkForUpdates(currentVersion, manifestURL string) {
	if manifestURL == "" {
		return
	}
	log.Printf("Проверка обновлений по адресу: %s", manifestURL)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(manifestURL)
	if err != nil {
		log.Printf("Ошибка при проверке обновлений: не удалось получить данные с сервера: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Ошибка при проверке обновлений: сервер вернул статус %d", resp.StatusCode)
		return
	}

	var info UpdateInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		log.Printf("Ошибка при проверке обновлений: не удалось разобрать JSON-манифест: %v", err)
		return
	}

	vCurrent, err := gover.NewVersion(currentVersion)
	if err != nil {
		log.Printf("Ошибка: некорректный формат текущей версии '%s': %v", currentVersion, err)
		return
	}
	vLatest, err := gover.NewVersion(info.Version)
	if err != nil {
		log.Printf("Ошибка: некорректный формат версии на сервере '%s': %v", info.Version, err)
		return
	}

	if vLatest.GreaterThan(vCurrent) {
		log.Printf("Доступна новая версия: %s. Текущая версия: %s. Начинаю обновление...", vLatest, vCurrent)

		downloadURL, err := resolveDownloadURL(manifestURL, info.Url)
		if err != nil {
			log.Printf("Некорректный URL для скачивания обновления: %v", err)
			return
		}
		log.Printf("URL для скачивания exe-файла: %s", downloadURL)

		restarted, err := doUpdate(info.Url, info.Sha256)
		if err != nil {
			log.Printf("Не удалось обновить приложение: %v", err)
		} else if restarted {
			log.Println("Приложение успешно обновлено и перезапущено. Текущий процесс завершается.")
			os.Exit(0)
		}
	} else {
		log.Printf("Установлена актуальная версия приложения (%s).", currentVersion)
	}
}

// Она объединяет URL манифеста с путем к файлу из JSON.
func resolveDownloadURL(base, path string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("не удалось разобрать базовый URL: %w", err)
	}

	// Парсим путь к файлу (он может быть как полным, так и относительным)
	pathURL, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("не удалось разобрать путь к файлу: %w", err)
	}

	// ResolveReference правильно соединяет базовый URL с путем
	// Если path - абсолютный URL, вернется он сам.
	// Если path - относительный, он будет добавлен к базовому.
	return baseURL.ResolveReference(pathURL).String(), nil
}

// doUpdate выполняет замену бинарного файла и перезапускает приложение.
func doUpdate(updateUrl, sha256sum string) (restarted bool, err error) {
	resp, err := http.Get(updateUrl)
	if err != nil {
		return false, fmt.Errorf("ошибка скачивания новой версии: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("сервер вернул ошибку при скачивании: %s", resp.Status)
	}

	opts := selfupdate.Options{
		Checksum: []byte(sha256sum),
		Hash:     crypto.SHA256,
	}

	if err := selfupdate.Apply(resp.Body, opts); err != nil {
		if rerr := selfupdate.RollbackError(err); rerr != nil {
			return false, fmt.Errorf("не удалось откатить обновление после ошибки: %v", rerr)
		}
		return false, fmt.Errorf("ошибка применения обновления: %w", err)
	}

	log.Println("Бинарный файл успешно обновлен. Запускаю новую версию...")

	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("не удалось получить путь к исполняемому файлу: %w", err)
	}

	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("не удалось запустить обновленное приложение: %w", err)
	}

	return true, nil
}

// cleanupOldVersion удаляет старый .old файл, оставшийся после успешного обновления.
func cleanupOldVersion() {
	time.Sleep(1 * time.Second)

	exePath, err := os.Executable()
	if err != nil {
		log.Printf("Предупреждение: не удалось определить путь к исполняемому файлу для очистки: %v", err)
		return
	}

	oldExePath := exePath + ".old"
	if _, err := os.Stat(oldExePath); err == nil {
		if err := os.Remove(oldExePath); err != nil {
			log.Printf("Предупреждение: не удалось удалить старую версию приложения ('%s'): %v", oldExePath, err)
		} else {
			log.Printf("Старая версия приложения ('%s') успешно удалена.", oldExePath)
		}
	}
}
