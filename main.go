package main

import (
	"fmt"
	"log"
	"net/http"
	"time"
	"github.com/gorilla/websocket"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func main() {
	http.HandleFunc("/", handleHome)
	http.HandleFunc("/ws", handleWebSocket)
	fmt.Println("服务器启动在 http://43.133.212.191:8080")
	log.Fatal(http.ListenAndServe("0.0.0.0:8080", nil))
}

func handleHome(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "index.html")
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket 升级错误:", err)
		return
	}
	defer conn.Close()
	
	log.Println("新的 WebSocket 连接已建立")
	
	for {
		cpuPercentage, _ := cpu.Percent(time.Second, false)
		ioCounters, _ := disk.IOCounters()
		
		var totalReadTime, totalWriteTime, totalReadCount, totalWriteCount uint64
		for _, counter := range ioCounters {
			totalReadTime += counter.ReadTime
			totalWriteTime += counter.WriteTime
			totalReadCount += counter.ReadCount
			totalWriteCount += counter.WriteCount
		}
		
		avgReadTime := float64(totalReadTime) / float64(totalReadCount)
		avgWriteTime := float64(totalWriteTime) / float64(totalWriteCount)
		avgIOTime := (avgReadTime + avgWriteTime) / 2
		
		data := fmt.Sprintf("%.2f,%.2f", cpuPercentage[0], avgIOTime)
		err = conn.WriteMessage(websocket.TextMessage, []byte(data))
		if err != nil {
			log.Println("写入消息错误:", err)
			return
		}
		
		time.Sleep(2 * time.Second)
	}
}
