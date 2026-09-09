package bohemia

type Player struct {
	UserID         string `json:"userId"`
	Username       string `json:"username"`
	GameClientType string `json:"gameClientType"`
	PlatformUserID string `json:"platformUserId"`
}

type ListPlayersResponse struct {
	ConnectedPlayers []Player `json:"connectedPlayers"`
	QueuePlayers     []Player `json:"queuePlayers"`
}
