// Package steam — сбор публичных данных Steam об игроках через шлюз arma-reforger-hz.
//
// Здесь НЕТ похода в Valve напрямую: ключ Steam Web API живёт в hz, и второе место,
// где он нужен, создавать незачем. Наша задача — забрать, разложить и сохранить.
package steam

import (
	"encoding/json"
	"time"
)

// Источники, которые отдаёт шлюз. Значения совпадают с полем source в ответе hz
// и с steam.snapshot.source в базе.
const (
	SourceSummary = "summary"
	SourceFriends = "friends"
	SourceGames   = "games"
	SourceBans    = "bans"
)

// ReforgerAppID — Arma Reforger в Steam. Единственная игра, которая нас интересует.
const ReforgerAppID = 1874880

// Snapshot — ответ шлюза целиком.
//
// Имена полей в camelCase, а не в snake_case, как в нашем собственном API: так их отдаёт
// arma-reforger-hz. ASP.NET по умолчанию сериализует свойства именно так, и Go-клиент токена
// давно живёт с тем же соглашением (`accessToken`). Несовпадение здесь ничем не выдаёт себя
// при разборе — json просто оставляет поле пустым, — и первым признаком было
// «gateway answered for "", asked for ...».
type Snapshot struct {
	SteamID   string    `json:"steamId"`
	FetchedAt time.Time `json:"fetchedAt"`
	Sources   []Source  `json:"sources"`
}

// Source — один метод Valve. Body пуст, если запрос не удался; Error тогда объясняет почему.
type Source struct {
	Name  string          `json:"source"`
	Body  json.RawMessage `json:"body"`
	Error string          `json:"error"`
}

// Body возвращает тело источника: найден ли он и пришёл ли непустым.
func (s Snapshot) Body(name string) (json.RawMessage, bool) {
	for _, src := range s.Sources {
		if src.Name == name {
			return src.Body, len(src.Body) > 0 && string(src.Body) != "null"
		}
	}
	return nil, false
}

// Profile — сведённое состояние игрока из всех источников. Поля-указатели там, где
// «неизвестно» и «ноль» — разные вещи: скрытые игры это не ноль часов, а отсутствие данных.
type Profile struct {
	SteamID          string
	PersonaName      string
	RealName         string
	AvatarHash       string
	ProfileURL       string
	CountryCode      string
	PrimaryClanID    string
	Visibility       *int
	AccountCreatedAt *time.Time

	VACBanned        *bool
	VACBanCount      *int
	GameBanCount     *int
	DaysSinceLastBan *int
	EconomyBan       string

	ReforgerMinutes   *int
	ReforgerMinutes2W *int

	GamesVisible   bool
	FriendsVisible bool
}

// Friend — ребро графа дружбы.
type Friend struct {
	SteamID string
	Since   *time.Time
}

// StoredProfile — состояние игрока, прочитанное из базы. Отличается от Profile наличием
// UpdatedAt: потребителю важно понимать, насколько данные свежие.
type StoredProfile struct {
	SteamID          string     `json:"steam_id"`
	PersonaName      string     `json:"persona_name"`
	RealName         string     `json:"real_name"`
	AvatarHash       string     `json:"avatar_hash"`
	ProfileURL       string     `json:"profile_url"`
	CountryCode      string     `json:"country_code"`
	PrimaryClanID    string     `json:"primary_clan_id"`
	Visibility       *int       `json:"visibility"`
	AccountCreatedAt *time.Time `json:"account_created_at"`

	VACBanned        *bool  `json:"vac_banned"`
	VACBanCount      *int   `json:"vac_ban_count"`
	GameBanCount     *int   `json:"game_ban_count"`
	DaysSinceLastBan *int   `json:"days_since_last_ban"`
	EconomyBan       string `json:"economy_ban"`

	ReforgerMinutes   *int `json:"reforger_minutes"`
	ReforgerMinutes2W *int `json:"reforger_minutes_2w"`

	GamesVisible   bool      `json:"games_visible"`
	FriendsVisible bool      `json:"friends_visible"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// KnownFriend — друг из Steam, при этом сопоставленный с нашим игроком, если он нам известен.
type KnownFriend struct {
	SteamID    string     `json:"steam_id"`
	Since      *time.Time `json:"since"`
	PlayerID   *int64     `json:"player_id"` // наш игрок, если друг встречался на наблюдаемых серверах
	Nickname   string     `json:"nickname"`  // его последний игровой ник
	LastSeenAt *time.Time `json:"last_seen_at"`
}

// Dossier — всё, что мы знаем об игроке со стороны Steam.
//
// Profile — УКАЗАТЕЛЬ, и это принципиально: null означает «данных ещё нет», а нулевая
// структура означала бы «профиль пустой и обновлён в первом году нашей эры». Потребитель
// на такой структуре честно нарисовал бы «библиотека скрыта» и «собрано 2025 лет назад» —
// ровно так и случилось, пока поле было значением.
type Dossier struct {
	// PlayerID — наш игрок. 0, если этот Steam-аккаунт у нас не наблюдался: досье можно
	// запросить и по произвольному SteamID, никак с Arma не связанному.
	PlayerID int64 `json:"player_id"`
	// Nickname — как этого человека зовут в игре у нас. Пусто вместе с PlayerID.
	Nickname     string         `json:"nickname,omitempty"`
	SteamID      string         `json:"steam_id"`
	Profile      *StoredProfile `json:"profile"`
	Friends      []KnownFriend  `json:"friends"`
	FriendsKnown int            `json:"friends_known"` // сколько из них встречались у нас
	Collected    bool           `json:"collected"`     // собирали ли прямо сейчас
	// LastError — почему сбор не удался, если он не удался. Человеку это объяснит,
	// почему досье пустое: «шлюз не настроен» и «профиль закрыт» — разные новости.
	LastError string `json:"last_error,omitempty"`
}
