// Package migrations хранит SQL-миграции схемы и отдаёт их как встроенную файловую систему,
// чтобы бинарник применял их сам, без файлов рядом (ADR 0003).
package migrations

import "embed"

// FS — все *.sql этого каталога, вшитые в бинарник на этапе компиляции.
//
//go:embed *.sql
var FS embed.FS
