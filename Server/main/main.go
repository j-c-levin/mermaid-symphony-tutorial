package main

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/autotls"
	"github.com/gin-gonic/gin"
	"gopkg.in/olahol/melody.v1"
	"log"
)

var m *melody.Melody
var sessionRoomMap = make(map[*melody.Session]string)
var roomPlayersMap = make(map[string]room)

type room struct {
	id      string
	master  string
	players []player
}

type player struct {
	session *melody.Session
	id      string
}

type message map[string]interface{}

func main() {
	// Gin is the server, melody is for websockets
	r := gin.Default()
	m = melody.New()

	// Healthcheck endpoint for testing
	r.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "alive",
		})
	})

	// Go to this path to connect to a websocket
	r.GET("/ws", func(c *gin.Context) {
		err := m.HandleRequest(c.Writer, c.Request)
		if err != nil {
			fmt.Printf("WS error: %s \n", err.Error())
		}
	})

	m.HandleDisconnect(func(s *melody.Session) {
		roomName := sessionRoomMap[s]
		room := roomPlayersMap[roomName]

		i := index(playerSessions(room), s)
		if i == -1 {
			fmt.Print("Handle disconnect error: index of session not found")
			return
		}

		leavingPlayer := room.players[i]

		room.players = append(room.players[:i], room.players[i+1:]...)

		delete(sessionRoomMap, s)

		leaverIsMaster := room.master == leavingPlayer.id
		roomIsNotEmpty := len(room.players) > 0
		if leaverIsMaster && roomIsNotEmpty {
			room.master = room.players[0].id
			shareNewMaster(room)
		}

		if roomIsNotEmpty {
			roomPlayersMap[roomName] = room
			resp := make(map[string]interface{})
			resp["command"] = "PLAYER_LEFT"
			resp["player_id"] = leavingPlayer
			msg := structToByte(resp)
			err := m.BroadcastMultiple(msg, playerSessions(room))
			if err != nil {
				fmt.Printf("player left error: %s \n", err.Error())
			}
			return
		}

		// Room is empty, reclaim it
		delete(roomPlayersMap, roomName)
	})

	// Run every time a client sends a websocket message
	m.HandleMessage(func(s *melody.Session, m []byte) {
		message := byteToStruct(m)
		switch message["command"] {
		case "CREATE_ROOM":
			createRoom(s, message)
		case "JOIN_ROOM":
			joinRoom(s, message)
		case "JOIN_RANDOM_ROOM":
			joinRandomRoom(s, message)
		case "MOVEMENT":
			broadcastToOthers(s, message)
		default:
			broadcastToRoom(s, message)
		}
	})

	log.Fatal(autotls.Run(r, "mermaidsymphony.eu"))
}

func broadcastToRoom(s *melody.Session, msg message) {
	room := sessionRoomMap[s]
	players := playerSessions(roomPlayersMap[room])
	err := m.BroadcastMultiple(structToByte(msg), players)
	if err != nil {
		fmt.Printf("broadcast to room error: %s \n", err.Error())
	}
}

func broadcastToOthers(s *melody.Session, msg message) {
	room := sessionRoomMap[s]
	players := filter(playerSessions(roomPlayersMap[room]), func(o *melody.Session) bool {
		return o != s
	})
	err := m.BroadcastMultiple(structToByte(msg), players)
	if err != nil {
		fmt.Printf("broadcast to others error: %s \n", err.Error())
	}
}

func shareNewMaster(r room) {
	resp := make(map[string]interface{})
	resp["command"] = "NEW_MASTER"
	resp["master"] = r.master
	msg := structToByte(resp)

	err := m.BroadcastMultiple(msg, playerSessions(r))
	if err != nil {
		fmt.Printf("respond room joined error: %s \n", err.Error())
	}
}

func respondRoomJoined(s *melody.Session, r room) {
	resp := make(map[string]interface{})
	resp["command"] = "ROOM_JOINED"
	resp["roomName"] = r.id
	resp["master"] = r.master
	resp["playerCount"] = len(r.players)
	msg := structToByte(resp)

	err := m.BroadcastMultiple(msg, []*melody.Session{s})
	if err != nil {
		fmt.Printf("respond room joined error: %s \n", err.Error())
	}
}

func joinRandomRoom(s *melody.Session, msg message) {
	for roomName := range roomPlayersMap {
		fmt.Printf("joining room %s \n", roomName)
		msg["data"].(map[string]interface{})["roomName"] = roomName
		joinRoom(s, msg)
		return
	}

	// Fallback if there are no current rooms
	msg["data"].(map[string]interface{})["roomName"] = "shua"
	createRoom(s, msg)
}

func joinRoom(s *melody.Session, msg message) {
	roomName := msg["data"].(map[string]interface{})["roomName"].(string)
	roomToJoin, ok := roomPlayersMap[roomName]
	if !ok {
		createRoom(s, msg)
		return
	}
	roomToJoin.players = append(roomToJoin.players, player{
		session: s,
		id:      msg["player_id"].(string),
	})
	roomPlayersMap[roomName] = roomToJoin

	sessionRoomMap[s] = roomName
	fmt.Printf("joined room %s", roomName)

	respondRoomJoined(s, roomPlayersMap[roomName])
}

func createRoom(s *melody.Session, msg message) {
	roomName := msg["data"].(map[string]interface{})["roomName"].(string)[0:4]
	roomPlayersMap[roomName] = room{
		id:     roomName,
		master: msg["player_id"].(string),
		players: []player{{
			session: s,
			id:      msg["player_id"].(string),
		}},
	}
	sessionRoomMap[s] = roomName
	fmt.Printf("created room %s", roomName)

	respondRoomJoined(s, roomPlayersMap[roomName])
}

func filter(vs []*melody.Session, f func(*melody.Session) bool) []*melody.Session {
	vsf := make([]*melody.Session, 0)
	for _, v := range vs {
		if f(v) {
			vsf = append(vsf, v)
		}
	}
	return vsf
}

func index(vs []*melody.Session, t *melody.Session) int {
	for i, v := range vs {
		if v == t {
			return i
		}
	}
	return -1
}

func byteToStruct(messageBytes []byte) message {
	var f interface{}
	err := json.Unmarshal(messageBytes, &f)
	if err != nil {
		fmt.Printf("byte to struct error: %s \n", err.Error())
	}

	return f.(map[string]interface{})
}

func structToByte(m message) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		fmt.Printf("struct to byte error: %s \n", err.Error())
	}
	return b
}

func playerSessions(r room) []*melody.Session {
	var players []*melody.Session
	for _, p := range r.players {
		players = append(players, p.session)
	}
	return players
}
