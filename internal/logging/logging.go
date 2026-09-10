// Package logging настраивает slog: JSON в stdout всегда (docker logs), и опционально
// в файл с ротацией — чтобы логи лежали рядом с приложением и не терялись при пересоздании контейнера.
package logging

import (
	"io"
	"log/slog"
	"os"

	"gopkg.in/natefinch/lumberjack.v2"
)

// FileConfig — файл логов с ротацией. Пустой Path — только stdout.
type FileConfig struct {
	Path       string
	MaxSizeMB  int // размер файла до ротации
	MaxBackups int // сколько старых файлов хранить
	MaxAgeDays int // сколько дней хранить старые файлы
}

// Setup ставит slog.Default. Возвращает функцию закрытия файла для defer в main.
func Setup(cfg FileConfig) (closeFn func() error) {
	var w io.Writer = os.Stdout
	closeFn = func() error { return nil }

	if cfg.Path != "" {
		file := &lumberjack.Logger{
			Filename:   cfg.Path,
			MaxSize:    max(cfg.MaxSizeMB, 1),
			MaxBackups: cfg.MaxBackups,
			MaxAge:     cfg.MaxAgeDays,
			Compress:   true,
		}
		w = io.MultiWriter(os.Stdout, file)
		closeFn = file.Close
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(w, nil)))
	return closeFn
}
