// probe — ручная проверка протокола на живом API: взять токен, найти сервер по адресу,
// показать комнату и список игроков. Не пишет в БД. Первый пункт MVP (AGENTS §23).
//
//	go run ./cmd/probe -host 37.48.253.41:2001
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"armaplayers/internal/app"
	"armaplayers/internal/bohemia"
	"armaplayers/internal/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "probe:", err)
		os.Exit(1)
	}
}

func run() error {
	host := flag.String("host", "", "hostAddress сервера в формате ip:port")
	text := flag.String("text", "", "подстрока имени сервера (вместо -host)")
	players := flag.Bool("players", true, "вызвать listPlayers для найденной комнаты")
	raw := flag.Bool("raw", false, "напечатать сырые JSON-ответы (для исследования полей)")
	flag.Parse()

	if *host == "" && *text == "" {
		return errors.New("укажи -host ip:port или -text имя")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	accessToken, err := app.NewTokenProvider(cfg).Get(ctx)
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}
	fmt.Printf("token: ok (%d chars)\n", len(accessToken))

	client := app.NewBohemiaClient(cfg.Bohemia)

	search, err := client.SearchRooms(ctx, accessToken, bohemia.RoomSearch{HostAddress: *host, Text: *text, Limit: 5})
	if err != nil {
		return fmt.Errorf("searchRooms: %w", err)
	}
	fmt.Printf("searchRooms: totalCount=%d searchFrom=%d rooms=%d\n", search.TotalCount, search.SearchFrom, len(search.Rooms))
	for i, r := range search.Rooms {
		fmt.Printf("  [%d] id=%s host=%s name=%q players=%d/%d joinCode=%s updated=%s\n",
			i, r.ID, r.HostAddress, r.Name, r.PlayerCount, r.PlayerCountLimit, r.DirectJoinCode,
			time.Unix(r.Updated, 0).UTC().Format(time.RFC3339))
		if r.JoinQueue != nil {
			fmt.Printf("      queue: %d/%d avgWait=%ds type=%s\n", r.JoinQueue.Size, r.JoinQueue.MaxSize, r.JoinQueue.PositionAvgWaitTime, r.JoinQueue.Type)
		}
	}
	fmt.Printf("raw search response: %d bytes\n", len(search.Raw))
	if *raw {
		fmt.Printf("--- raw searchRooms ---\n%s\n", search.Raw)
	}

	if !*players || len(search.Rooms) == 0 {
		return nil
	}

	room := search.Rooms[0]
	list, err := client.ListPlayers(ctx, accessToken, room.ID)
	if err != nil {
		return fmt.Errorf("listPlayers: %w", err)
	}
	fmt.Printf("listPlayers: connected=%d queue=%d\n", len(list.ConnectedPlayers), len(list.QueuePlayers))
	if *raw {
		fmt.Printf("--- raw listPlayers (first 600 bytes) ---\n%.600s\n", list.Raw)
	}
	for _, p := range list.ConnectedPlayers {
		fmt.Printf("  %-40s %s %s %s\n", p.Username, p.UserID, p.GameClientType, p.PlatformUserID)
	}
	for _, p := range list.QueuePlayers {
		fmt.Printf("  [queue] %-32s %s %s %s\n", p.Username, p.UserID, p.GameClientType, p.PlatformUserID)
	}
	return nil
}
