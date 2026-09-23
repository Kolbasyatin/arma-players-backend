package config_test

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"armaplayers/internal/config"
)

// Переменные, которые в deploy/compose.yaml задаются НАПРЯМУЮ, а не подстановкой ${...}.
// Все они используются приложением как обычно — просто их значение диктует устройство
// контейнера, а не администратор, и класть их в deploy/.env бессмысленно: environment
// всё равно перекроет.
var setDirectlyInCompose = map[string]string{
	"DATABASE_URL": "строка собирается на месте из POSTGRES_PASSWORD",
	"HTTP_ADDR":    "порт внутри контейнера фиксирован, наружу отдаётся через ports",
	"LOG_FILE":     "путь внутри контейнера фиксирован (/logs/observer.log)",
}

// Конфигурация расползается тихо: добавил поле в Config — и оно работает локально, потому что
// os.Getenv видит твоё окружение, но на проде его неоткуда взять. Так уже случалось:
// STEAM_* приехали в compose, но не в deploy/.env.example, а BOHEMIA_* не пробрасывались
// вовсе — то есть после обновления Arma версию клиента нельзя было поправить без пересборки.
func TestEnvExamplesCoverEveryConfigField(t *testing.T) {
	expected := envNames(reflect.TypeOf(config.Config{}), "")
	if len(expected) < 20 {
		t.Fatalf("подозрительно мало переменных найдено: %d", len(expected))
	}

	root := declaredIn(t, "../../.env.example")
	deployEnv := declaredIn(t, "../../deploy/.env.example")
	compose := substitutedIn(t, "../../deploy/compose.yaml")

	for _, name := range expected {
		if !root[name] {
			t.Errorf("%s не описан в .env.example", name)
		}
		if reason, direct := setDirectlyInCompose[name]; direct {
			t.Logf("%s: подстановки в compose нет намеренно — %s", name, reason)
			continue
		}
		if !compose[name] {
			t.Errorf("%s не пробрасывается в deploy/compose.yaml — на проде его не задать", name)
		}
		if !deployEnv[name] {
			t.Errorf("%s не описан в deploy/.env.example", name)
		}
	}

	// И обратно: в compose не должно быть подстановок, которых нет в конфиге, — это опечатка
	// или забытая переменная, и она молча превратится в пустую строку.
	known := map[string]bool{}
	for _, name := range composeOnly {
		known[name] = true
	}
	for _, name := range expected {
		known[name] = true
	}
	for name := range compose {
		if !known[name] {
			t.Errorf("в compose подставляется %s, которого нет в Config", name)
		}
	}
}

// Переменные, которые читает только docker compose: в контейнер они не попадают и приложению
// неизвестны. Живут в конце deploy/.env.example, в корневом их быть не должно.
var composeOnly = []string{"POSTGRES_PASSWORD", "OBSERVER_TAG", "API_BIND_ADDR", "RUN_UID", "RUN_GID"}

// Два примера конфигурации — не два разных конфига, а один список переменных для двух площадок.
// Отличаться они должны только значениями: набор и ПОРЯДОК обязаны совпадать, иначе читать их
// рядом невозможно и расхождения накапливаются незаметно (так и вышло с STEAM_* и BOHEMIA_*).
func TestEnvExamplesHaveTheSameShape(t *testing.T) {
	appVars := map[string]bool{}
	for _, name := range envNames(reflect.TypeOf(config.Config{}), "") {
		appVars[name] = true
	}

	root := orderedIn(t, "../../.env.example", appVars)
	deploy := orderedIn(t, "../../deploy/.env.example", appVars)

	if len(root) == 0 || len(deploy) == 0 {
		t.Fatalf("переменные не разобрались: root=%d deploy=%d", len(root), len(deploy))
	}

	// Те, что в проде задаются самим compose, в его примере не повторяются.
	var expectedInDeploy []string
	for _, name := range root {
		if _, direct := setDirectlyInCompose[name]; direct {
			continue
		}
		expectedInDeploy = append(expectedInDeploy, name)
	}

	if strings.Join(deploy, ",") != strings.Join(expectedInDeploy, ",") {
		t.Errorf("порядок и состав переменных разошлись\n корневой (без compose-only): %v\n deploy:                      %v",
			expectedInDeploy, deploy)
	}

	// Compose-механика не должна протекать в конфигурацию для локального запуска.
	rootAll := declaredIn(t, "../../.env.example")
	for _, name := range composeOnly {
		if rootAll[name] {
			t.Errorf("%s читает только docker compose — в корневом .env.example ему не место", name)
		}
	}
}

// orderedIn — переменные приложения в порядке их появления в файле.
func orderedIn(t *testing.T, path string, appVars map[string]bool) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range assignment.FindAllStringSubmatch(string(raw), -1) {
		if appVars[m[1]] {
			names = append(names, m[1])
		}
	}
	return names
}

// envNames собирает имена из тегов env, разворачивая envPrefix вложенных структур.
func envNames(t reflect.Type, prefix string) []string {
	var names []string
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Type.Kind() == reflect.Struct {
			names = append(names, envNames(field.Type, prefix+field.Tag.Get("envPrefix"))...)
			continue
		}
		tag := field.Tag.Get("env")
		if tag == "" {
			continue
		}
		// env:"NAME,required" и env:"NAME"
		names = append(names, prefix+strings.SplitN(tag, ",", 2)[0])
	}
	return names
}

var (
	assignment   = regexp.MustCompile(`(?m)^([A-Z_0-9]+)=`)
	substitution = regexp.MustCompile(`\$\{([A-Z_0-9]+)`)
)

func declaredIn(t *testing.T, path string) map[string]bool {
	t.Helper()
	return match(t, path, assignment)
}

func substitutedIn(t *testing.T, path string) map[string]bool {
	t.Helper()
	return match(t, path, substitution)
}

func match(t *testing.T, path string, re *regexp.Regexp) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		found[m[1]] = true
	}
	return found
}
