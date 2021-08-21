package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/pion/webrtc/v3/pkg/media/ivfreader"
)

const homeHTML = `<!DOCTYPE html>
<html lang="en">
	<head>
		<title>synced-playback</title>
	</head>
	<body id="body">
		<video id="video" autoplay playsinline></video>

		<script>
			let conn = new WebSocket('ws://' + window.location.host + '/ws')
			let pc = new RTCPeerConnection()

			pc.ontrack = function (event) {
			  if (event.track.kind === 'audio') {
				return
			  }

			  var el = document.getElementById('video')
			  el.srcObject = event.streams[0]
			  el.autoplay = true
			  el.controls = true
			}

			conn.onopen = () => {
				console.log('Connection open')
			}
			conn.onclose = evt => {
				console.log('Connection closed')
			}
			conn.onmessage = evt => {
				let msg = JSON.parse(evt.data)
				if (!msg) {
					return console.log('failed to parse msg')
				}

				switch (msg.event) {
				case 'offer':
					offer = JSON.parse(msg.data)
					if (!offer) {
						return console.log('failed to parse answer')
					}
					pc.setRemoteDescription(offer)

					pc.createAnswer().then(answer => {
						pc.setLocalDescription(answer)
						conn.send(JSON.stringify({event: 'answer', data: JSON.stringify(answer)}))
					})
				}
			}
			window.conn = conn
		</script>
	</body>
</html>
`

//nolint
var (
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	peerConnectionConfig = webrtc.Configuration{}

	videoTrack = &webrtc.TrackLocalStaticSample{}
)

type websocketMessage struct {
	Event string `json:"event"`
	Data  string `json:"data"`
}

func main() {
	var err error
	videoTrack, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "video")
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		file, ivfErr := os.Open("output.ivf")
		if ivfErr != nil {
			panic(ivfErr)
		}

		ivf, header, ivfErr := ivfreader.NewWith(file)
		if ivfErr != nil {
			panic(ivfErr)
		}

		ticker := time.NewTicker(time.Millisecond * time.Duration((float32(header.TimebaseNumerator)/float32(header.TimebaseDenominator))*1000))
		for ; true; <-ticker.C {
			frame, _, ivfErr := ivf.ParseNextFrame()
			if ivfErr == io.EOF {
				fmt.Printf("All video frames parsed and sent")
				os.Exit(0)
			}

			if ivfErr != nil {
				panic(ivfErr)
			}

			if ivfErr = videoTrack.WriteSample(media.Sample{Data: frame, Duration: time.Second}); ivfErr != nil {
				panic(ivfErr)
			}
		}
	}()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, homeHTML)
	})
	http.HandleFunc("/ws", serveWs)

	log.Fatal(http.ListenAndServe(":8080", nil))
}

func sendOffer(pc *webrtc.PeerConnection, ws *websocket.Conn) error {
	answer, err := pc.CreateOffer(nil)
	if err != nil {
		return err
	}

	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if setErr := pc.SetLocalDescription(answer); setErr != nil {
		return setErr
	}
	<-gatherComplete

	answerString, err := json.Marshal(pc.LocalDescription())
	if err != nil {
		return err
	}

	return ws.WriteJSON(&websocketMessage{
		Event: "offer",
		Data:  string(answerString),
	})
}

func handleWebsocketMessage(pc *webrtc.PeerConnection, message *websocketMessage) error {
	if message.Event == "answer" {
		offer := webrtc.SessionDescription{}
		if err := json.Unmarshal([]byte(message.Data), &offer); err != nil {
			return err
		}

		return pc.SetRemoteDescription(offer)
	}
	return nil
}

func serveWs(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		if _, ok := err.(websocket.HandshakeError); !ok {
			log.Println(err)
		}
		return
	}

	peerConnection, err := webrtc.NewPeerConnection(peerConnectionConfig)
	if err != nil {
		log.Print(err)
		return
	}

	defer func() {
		if closeErr := peerConnection.Close(); closeErr != nil {
			log.Println(closeErr)
		}
	}()

	if _, err = peerConnection.CreateDataChannel("HelloWorld", nil); err != nil {
		log.Println(err)
		return
	}

	if err = sendOffer(peerConnection, ws); err != nil {
		log.Println(err)
		return
	}

	go trackAddRemoveLoop(peerConnection, ws)

	message := &websocketMessage{}
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			break
		} else if err := json.Unmarshal(msg, &message); err != nil {
			log.Print(err)
			return
		}

		if err := handleWebsocketMessage(peerConnection, message); err != nil {
			log.Print(err)
		}
	}
}

func trackAddRemoveLoop(peerConnection *webrtc.PeerConnection, ws *websocket.Conn) {
	var (
		currentRTPSender *webrtc.RTPSender
		err              error
	)

	ticker := time.NewTicker(time.Second * 5)
	for range ticker.C {
		fmt.Printf("removing track: %v\n", currentRTPSender == nil)
		if currentRTPSender == nil {
			if currentRTPSender, err = peerConnection.AddTrack(videoTrack); err != nil {
				panic(err)
			}
		} else {
			if err = peerConnection.RemoveTrack(currentRTPSender); err != nil {
				panic(err)
			}
			currentRTPSender = nil
		}

		if err = sendOffer(peerConnection, ws); err != nil {
			panic(err)
		}
	}
}
