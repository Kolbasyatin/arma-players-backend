package bohemia

// Player — объект игрока из listPlayers (connectedPlayers и queuePlayers).
type Player struct {
	UserID         string `json:"userId"`
	Username       string `json:"username"`
	GameClientType string `json:"gameClientType"`
	PlatformUserID string `json:"platformUserId"` // для PLATFORM_PC — SteamID64
}

// ListPlayersResponse — ответ rooms/listPlayers.
type ListPlayersResponse struct {
	ConnectedPlayers []Player `json:"connectedPlayers"`
	QueuePlayers     []Player `json:"queuePlayers"`

	// Raw — тело ответа как получено; для raw payload retention (ADR 0003, AGENTS §12).
	Raw []byte `json:"-"`
}

// Room — комната (игровая сессия сервера) из rooms/search.
// Поля сняты с реального ответа 1.8.0.13; неизвестные Bohemia-поля игнорируются.
type Room struct {
	ID                       string        `json:"id"` // roomId — id текущей сессии, не идентичность сервера (ADR 0004)
	Name                     string        `json:"name"`
	ScenarioID               string        `json:"scenarioId"`
	ScenarioName             string        `json:"scenarioName"`
	GameVersion              string        `json:"gameVersion"`
	HostType                 string        `json:"hostType"`
	HostAddress              string        `json:"hostAddress"` // ip:port игрового порта
	Official                 bool          `json:"official"`
	Joinable                 bool          `json:"joinable"`
	Visible                  bool          `json:"visible"`
	PasswordProtected        bool          `json:"passwordProtected"`
	BattlEye                 bool          `json:"battlEye"`
	PlayerCount              int           `json:"playerCount"`
	PlayerCountLimit         int           `json:"playerCountLimit"`
	DirectJoinCode           string        `json:"directJoinCode"`
	PlatformName             string        `json:"platformName"`
	PingSiteID               string        `json:"pingSiteId"`
	SessionID                string        `json:"sessionId"`
	SupportedGameClientTypes []string      `json:"supportedGameClientTypes"`
	Mods                     []Mod         `json:"mods"`
	HostedScenarioModID      string        `json:"hostedScenarioModId"` // мод, содержащий сценарий; пусто для ванильных
	RuntimeStats             *RuntimeStats `json:"runtimeStats"`
	JoinQueue                *JoinQueue    `json:"joinQueue"`
	Favorite                 bool          `json:"favorite"`         // относится к аккаунту, чьим токеном ходим; для наблюдения бесполезно
	Flags                    int           `json:"flags"`            // битовая маска, значение бит не исследовано (наблюдалось 1)
	Updated                  int64         `json:"updated"`          // unix seconds, последний heartbeat сервера в каталог
	DetailsUpdatedAt         int64         `json:"detailsUpdatedAt"` // unix seconds, последнее изменение описания комнаты
	LastJoinedAt             int64         `json:"lastJoinedAt"`     // unix seconds, встречается не у всех комнат
}

// Mod — элемент списка mods комнаты.
type Mod struct {
	ModID   string `json:"modId"` // 16 hex-символов, id в Workshop
	Name    string `json:"name"`
	Version string `json:"version"`
}

// JoinQueue — состояние очереди на вход.
type JoinQueue struct {
	Type                string `json:"type"` // наблюдалось: REGULAR
	Size                int    `json:"size"`
	MaxSize             int    `json:"maxSize"`
	PositionAvgWaitTime int    `json:"positionAvgWaitTime"`
}

// RuntimeStats — телеметрия сервера из heartbeat.
type RuntimeStats struct {
	Memory int `json:"memory"`
	FPS    int `json:"fps"`
}

// SearchRoomsResponse — ответ rooms/search.
type SearchRoomsResponse struct {
	Rooms      []Room `json:"rooms"`
	SearchFrom int    `json:"searchFrom"`
	TotalCount int    `json:"totalCount"`

	Raw []byte `json:"-"`
}
