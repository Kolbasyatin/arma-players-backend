package steam

import (
	"encoding/json"
	"time"
)

// Разбор ответов Valve. Отдельно от сохранения намеренно: сырьё должно ложиться в базу,
// даже если разбор сломался. Valve переименует поле — сломается извлечение, а не сбор,
// и починить можно будет по уже сохранённым снимкам, не дожидаясь следующего опроса.
//
// Все структуры ниже — ЧАСТИЧНЫЕ: описаны только нужные поля, остальное игнорируется.
// Так изменение чужой формы в неиспользуемой части нас не задевает.

type summaryResponse struct {
	Response struct {
		Players []struct {
			SteamID                  string `json:"steamid"`
			PersonaName              string `json:"personaname"`
			RealName                 string `json:"realname"`
			AvatarHash               string `json:"avatarhash"`
			ProfileURL               string `json:"profileurl"`
			LocCountryCode           string `json:"loccountrycode"`
			PrimaryClanID            string `json:"primaryclanid"`
			CommunityVisibilityState *int   `json:"communityvisibilitystate"`
			TimeCreated              *int64 `json:"timecreated"`
		} `json:"players"`
	} `json:"response"`
}

// Valve отвечает здесь в PascalCase, в отличие от всех остальных методов.
// Это не опечатка: GetPlayerBans действительно оформлен иначе, чем соседние эндпоинты.
type bansResponse struct {
	Players []struct {
		SteamID          string `json:"SteamId"`
		CommunityBanned  *bool  `json:"CommunityBanned"`
		VACBanned        *bool  `json:"VACBanned"`
		NumberOfVACBans  *int   `json:"NumberOfVACBans"`
		DaysSinceLastBan *int   `json:"DaysSinceLastBan"`
		NumberOfGameBans *int   `json:"NumberOfGameBans"`
		EconomyBan       string `json:"EconomyBan"`
	} `json:"players"`
}

type gamesResponse struct {
	Response struct {
		GameCount *int `json:"game_count"`
		Games     []struct {
			AppID           int  `json:"appid"`
			PlaytimeForever *int `json:"playtime_forever"`
			Playtime2Weeks  *int `json:"playtime_2weeks"`
		} `json:"games"`
	} `json:"response"`
}

type friendsResponse struct {
	FriendsList struct {
		Friends []struct {
			SteamID      string `json:"steamid"`
			Relationship string `json:"relationship"`
			FriendSince  *int64 `json:"friend_since"`
		} `json:"friends"`
	} `json:"friendslist"`
}

// ParseProfile сводит снимок в одну строку состояния. Ошибку не возвращает: непрочитанный
// источник — не повод потерять остальные три. Что не разобралось, остаётся пустым.
func ParseProfile(snapshot Snapshot) Profile {
	profile := Profile{SteamID: snapshot.SteamID}

	if body, ok := snapshot.Body(SourceSummary); ok {
		var parsed summaryResponse
		if json.Unmarshal(body, &parsed) == nil && len(parsed.Response.Players) > 0 {
			player := parsed.Response.Players[0]
			profile.PersonaName = player.PersonaName
			profile.RealName = player.RealName
			profile.AvatarHash = player.AvatarHash
			profile.ProfileURL = player.ProfileURL
			profile.CountryCode = player.LocCountryCode
			profile.PrimaryClanID = player.PrimaryClanID
			profile.Visibility = player.CommunityVisibilityState
			if player.TimeCreated != nil {
				created := time.Unix(*player.TimeCreated, 0).UTC()
				profile.AccountCreatedAt = &created
			}
		}
	}

	if body, ok := snapshot.Body(SourceBans); ok {
		var parsed bansResponse
		if json.Unmarshal(body, &parsed) == nil && len(parsed.Players) > 0 {
			player := parsed.Players[0]
			profile.VACBanned = player.VACBanned
			profile.VACBanCount = player.NumberOfVACBans
			profile.GameBanCount = player.NumberOfGameBans
			profile.DaysSinceLastBan = player.DaysSinceLastBan
			profile.EconomyBan = player.EconomyBan
		}
	}

	if body, ok := snapshot.Body(SourceGames); ok {
		var parsed gamesResponse
		if json.Unmarshal(body, &parsed) == nil {
			// Скрытые игры Valve отдаёт как HTTP 200 и {"response":{}} — без game_count и без
			// списка. Отличить «скрыто» от «нет ни одной игры» можно ТОЛЬКО по наличию game_count.
			profile.GamesVisible = parsed.Response.GameCount != nil
			for _, game := range parsed.Response.Games {
				if game.AppID == ReforgerAppID {
					profile.ReforgerMinutes = game.PlaytimeForever
					profile.ReforgerMinutes2W = game.Playtime2Weeks
					break
				}
			}
		}
	}

	if _, ok := snapshot.Body(SourceFriends); ok {
		profile.FriendsVisible = true
	}

	return profile
}

// ParseFriends достаёт рёбра графа. Второй возвращаемый признак — виден ли список вообще:
// пустой список у открытого профиля и закрытый профиль — разные вещи.
func ParseFriends(snapshot Snapshot) ([]Friend, bool) {
	body, ok := snapshot.Body(SourceFriends)
	if !ok {
		return nil, false
	}

	var parsed friendsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, false
	}

	friends := make([]Friend, 0, len(parsed.FriendsList.Friends))
	for _, entry := range parsed.FriendsList.Friends {
		// relationship бывает не только "friend" (есть ещё заявки), а нас интересует дружба.
		if entry.SteamID == "" || entry.Relationship != "friend" {
			continue
		}
		friend := Friend{SteamID: entry.SteamID}
		if entry.FriendSince != nil && *entry.FriendSince > 0 {
			since := time.Unix(*entry.FriendSince, 0).UTC()
			friend.Since = &since
		}
		friends = append(friends, friend)
	}

	return friends, true
}
