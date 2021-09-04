package main

import (
	"fmt"
	"os"
	"time"
	"log"
	"net/http"
	"io"

	"encoding/json"
	"github.com/gorilla/websocket"

//	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
//	"github.com/pion/webrtc/v3/examples/internal/signal"
)

const homeHTML = `<!DOCTYPE html>
<html lang="en">
	<head>
		<title>synced-playback</title>
	</head>
	<style>
		table {
			width: 100%;
			background: #ccc;
		}
		td {
			width: 50%;
			padding: 0.25em;
			text-align: center;
		}
		video {
			background: #000;
			width: 100%;
		}
		.controls {
			background: #ccc;
			padding: 0.5em;
			margin-top: 1em;
		}
	</style>
	<body>
		<table>
			<tbody>
				<tr><td>Local</td><td>Loopback</td></td>
				<tr>
					<td><video id="localVideo" autoplay playsinline mute></video></td>
					<td><video id="remoteVideo" autoplay playsinline mute></video></td>
				</tr>
			</tbody>
		<table>
		<div class="controls">
			<button id="start">Start</button>
			<button id="stop">Stop</button>
		</div>

		<script>
let conn = new WebSocket('ws://' + window.location.host + '/ws');
let pc = new RTCPeerConnection({sdpSemantics: "unified-plan"});
let cameraTrack = null;
let localVideo = document.getElementById("localVideo");
let remoteVideo = document.getElementById("remoteVideo");

document.getElementById("start").addEventListener("click", function() {
    if (cameraTrack === null)
        startCamera();
});

document.getElementById("stop").addEventListener("click", function() {
    if (cameraTrack !== null)
        stopCamera();
});

function startCamera() {
    navigator.mediaDevices.getUserMedia({video: true, audio: false}).then(function(stream) {
        let videoTracks = stream.getVideoTracks();
        if (videoTracks.length !== 1) {
            console.log("Cannot acquire webcam video track.");
            return;
        }

        localVideo.srcObject = stream;
        cameraTrack = videoTracks[0];
        pc.addTrack(videoTracks[0]);

        console.log("Camera started.");
    }).catch(function(exception) {
        console.log(exception);
    });
}

function stopCamera() {
    if (cameraTrack === null)
        return;

    let senders = pc.getSenders();
    for (let i = 0; i < senders.length; ++i) {
        if (senders[i].track === cameraTrack) {
            pc.removeTrack(senders[i]);
            cameraTrack = null;
            localVideo.srcObject = null;
            remoteVideo.srcObject = null;
            console.log("Camera stopped.");
            return;
        }
    }

    console.log("Cannot find sender for track", cameraTrack);
}

pc.onnegotiationneeded = function () {
    pc.createOffer().then(function(offer) {
        return pc.setLocalDescription(offer);
    }).then(function(success) {
        if (success !== false) {
            console.log("Signal offer to server", pc.localDescription);
            conn.send(JSON.stringify({event: 'offer', data: JSON.stringify(pc.localDescription)}));
        }
    }).catch(function(exception) {
        console.log(exception);
    });
}

pc.ontrack = function (event) {
    if (event.track.kind === 'audio')
        return;

    console.log("New remote video track.");
    remoteVideo.srcObject = event.streams[0];
}

conn.onopen = () => {
    console.log('Connection open');
}
conn.onclose = evt => {
    console.log('Connection closed');
}
conn.onmessage = evt => {
    let msg = JSON.parse(evt.data);
    if (!msg) {
        return console.log("failed to parse msg");
    }

    switch (msg.event) {
        case "answer":
            console.log("Answer received.");
            let answer = JSON.parse(msg.data);
            if (!answer) {
                console.log("failed to parse answer");
                return;
            }
            pc.setRemoteDescription(answer).catch(function (exception) {
                console.log(exception);
            });
            break;

        default:
            console.log("unhandled websocket event", msg.event);
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
	peerConnection *webrtc.PeerConnection
	//videoTrack = &webrtc.TrackLocalStaticSample{}
)

type websocketMessage struct {
	Event string `json:"event"`
	Data  string `json:"data"`
}

func main() {
	go setupPeerConnection()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, homeHTML)
	})
	http.HandleFunc("/ws", serveWs)

	log.Fatal(http.ListenAndServe(":8080", nil))
}

func sendOffer(pc *webrtc.PeerConnection, ws *websocket.Conn) error {
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return err
	}

	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if setErr := pc.SetLocalDescription(offer); setErr != nil {
		return setErr
	}
	<-gatherComplete

	offerString, err := json.Marshal(pc.LocalDescription())
	if err != nil {
		return err
	}

	log.Printf("Offer sent to browser.");
	return ws.WriteJSON(&websocketMessage{
		Event: "offer",
		Data:  string(offerString),
	})
}

func sendAnswer(pc *webrtc.PeerConnection, ws *websocket.Conn) error {
	answer, err := pc.CreateAnswer(nil)
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

	log.Printf("Answer sent to browser.");
	return ws.WriteJSON(&websocketMessage{
		Event: "answer",
		Data:  string(answerString),
	})
}

func handleWebsocketMessage(pc *webrtc.PeerConnection, message *websocketMessage, ws *websocket.Conn) error {
	if message.Event == "answer" || message.Event == "offer" {
		log.Printf("Received %v\n", message.Event)

		sdp := webrtc.SessionDescription{}
		if err := json.Unmarshal([]byte(message.Data), &sdp); err != nil {
			return err
		}

		if err := pc.SetRemoteDescription(sdp); err != nil {
			return err
		}

		if message.Event == "answer" {
			if err := sendOffer(pc, ws); err != nil {
				return err
			}
		} else {
			if err := sendAnswer(pc, ws); err != nil {
				return err
			}
		}
	} else {
		log.Println("Unexpected websocket message.")
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

	message := &websocketMessage{}
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			break
		} else if err := json.Unmarshal(msg, &message); err != nil {
			log.Print(err)
			return
		}

		if err := handleWebsocketMessage(peerConnection, message, ws); err != nil {
			log.Print(err)
		}
	}
}

func setupPeerConnection() {
	var err error

	// Create a new RTCPeerConnection
	peerConnection, err = webrtc.NewPeerConnection(peerConnectionConfig)
	if err != nil {
		panic(err)
	}
	defer func() {
		if cErr := peerConnection.Close(); cErr != nil {
			fmt.Printf("cannot close peerConnection: %v\n", cErr)
		}
	}()

	// Create Track that we send video back to browser on
	outputTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "pion")
	if err != nil {
		panic(err)
	}

	// Add this newly created track to the PeerConnection
	rtpSender, err := peerConnection.AddTrack(outputTrack)
	if err != nil {
		panic(err)
	}

	// Read incoming RTCP packets
	// Before these packets are returned they are processed by interceptors. For things
	// like NACK this needs to be called.
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()

	// Set a handler for when a new remote track starts, this handler copies inbound RTP packets,
	// replaces the SSRC and sends them back
	peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		// Send a PLI on an interval so that the publisher is pushing a keyframe every rtcpPLIInterval
		// This is a temporary fix until we implement incoming RTCP events, then we would push a PLI only when a viewer requests it
		go func() {
			ticker := time.NewTicker(time.Second * 3)
			for range ticker.C {
				errSend := peerConnection.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(track.SSRC())}})
				if errSend != nil {
					fmt.Println(errSend)
				}
			}
		}()

		fmt.Printf("Track %s has started, of type %d: %s \n", track.ID(), track.PayloadType(), track.Codec().MimeType)
		for {
			// Read RTP packets being sent to Pion
			rtp, _, readErr := track.ReadRTP()

			if readErr == io.EOF {
				fmt.Printf("Track %s ended.", track.ID())
				break
			}

			if readErr != nil {
				panic(readErr)
			}

			if writeErr := outputTrack.WriteRTP(rtp); writeErr != nil {
				panic(writeErr)
			}
		}
	})

	// Set the handler for Peer connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		fmt.Printf("Peer Connection State has changed: %s\n", s.String())

		if s == webrtc.PeerConnectionStateFailed {
			// Wait until PeerConnection has had no network activity for 30 seconds or another failure. It may be reconnected using an ICE Restart.
			// Use webrtc.PeerConnectionStateDisconnected if you are interested in detecting faster timeout.
			// Note that the PeerConnection may come back from PeerConnectionStateDisconnected.
			fmt.Println("Peer Connection has gone to failed exiting")
			os.Exit(0)
		}
	})

	// Block forever
	select {}
}