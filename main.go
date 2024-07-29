package main

import (
	"database/sql"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
	"github.com/gorilla/websocket"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	_ "github.com/lib/pq"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var db *sql.DB

func main() {
	log.Printf("DB_USER: %s", os.Getenv("DB_USER"))
	log.Printf("DB_HOST: %s", os.Getenv("DB_HOST"))
	log.Printf("DB_PORT: %s", os.Getenv("DB_PORT"))
	log.Printf("DB_NAME: %s", os.Getenv("DB_NAME"))
	log.Printf("DB_PASSWORD length: %d", len(os.Getenv("DB_PASSWORD")))

	var err error
	dbURL := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		os.Getenv("DB_USER"),
		os.Getenv("DB_PASSWORD"),
		os.Getenv("DB_HOST"),
		os.Getenv("DB_PORT"),
		os.Getenv("DB_NAME"))
	
	log.Printf("Attempting to connect with URL: %s", dbURL)

	db, err = sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatal("数据库连接错误:", err)
	}
	defer db.Close()

	err = db.Ping()
	if err != nil {
		log.Fatal("无法连接到数据库:", err)
	}
	log.Println("成功连接到数据库")

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS test_table (
			id SERIAL PRIMARY KEY,
			data TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		log.Fatal("创建测试表错误:", err)
	}
	log.Println("测试表创建成功")

	go backgroundTasks()

	http.HandleFunc("/", handleHome)
	http.HandleFunc("/ws", handleWebSocket)
	http.HandleFunc("/backup", handleBackup)
	http.HandleFunc("/restore", handleRestore)
	http.HandleFunc("/compare", handleCompare)
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
		
		var avgIOTime float64
		if totalReadCount+totalWriteCount > 0 {
			avgIOTime = float64(totalReadTime+totalWriteTime) / float64(totalReadCount+totalWriteCount)
		}

		activeConnections, cacheHitRatio, transactionCommitRate := getPostgresMetrics()
		
		data := fmt.Sprintf("%.2f,%.2f,%.2f,%.2f,%.2f", cpuPercentage[0], avgIOTime, activeConnections, cacheHitRatio, transactionCommitRate)
		err = conn.WriteMessage(websocket.TextMessage, []byte(data))
		if err != nil {
			log.Println("写入消息错误:", err)
			return
		}
		
		time.Sleep(time.Second)
	}
}
func getPostgresMetrics() (float64, float64, float64) {
	var activeConnections, cacheHitRatio, transactionCommitRate float64

	err := db.QueryRow("SELECT COUNT(*) FROM pg_stat_activity WHERE state = 'active'").Scan(&activeConnections)
	if err != nil {
		log.Println("获取活跃连接数错误:", err)
	} else {
		log.Printf("活跃连接数: %.2f", activeConnections)
	}

	err = db.QueryRow(`
		SELECT 
			CASE WHEN blks_hit + blks_read = 0 THEN 0
			ELSE 100.0 * blks_hit / (blks_hit + blks_read) END AS cache_hit_ratio
		FROM pg_stat_database
		WHERE datname = current_database()
	`).Scan(&cacheHitRatio)
	if err != nil {
		log.Println("获取缓存命中率错误:", err)
	} else {
		log.Printf("缓存命中率: %.2f%%", cacheHitRatio)
	}

	var xactCommit, xactRollback int64
	err = db.QueryRow(`
		SELECT xact_commit, xact_rollback
		FROM pg_stat_database
		WHERE datname = current_database()
	`).Scan(&xactCommit, &xactRollback)
	if err != nil {
		log.Println("获取事务数据错误:", err)
	} else if xactCommit+xactRollback > 0 {
		transactionCommitRate = float64(xactCommit) * 100.0 / float64(xactCommit+xactRollback)
		log.Printf("事务提交率: %.2f%% (提交: %d, 回滚: %d)", transactionCommitRate, xactCommit, xactRollback)
	} else {
		log.Println("没有事务数据")
	}

	return activeConnections, cacheHitRatio, transactionCommitRate
}

func handleBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cmd := exec.Command("pg_dump", "-U", "postgres", "-d", "postgres", "-t", "pgbench_accounts", "-f", "pgbench_accounts_backup.sql")
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("备份错误: %v\n", err)
		fmt.Fprintf(w, "备份失败:\n%s\n%v", output, err)
		return
	}

	log.Println("备份成功")
	fmt.Fprintf(w, "备份成功:\n%s", output)
}

func handleRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	dropCmd := exec.Command("psql", "-U", "postgres", "-d", "postgres", "-c", "DROP TABLE IF EXISTS pgbench_accounts;")
	dropOutput, err := dropCmd.CombinedOutput()
	if err != nil {
		log.Printf("删除表错误: %v\n", err)
		fmt.Fprintf(w, "恢复失败 (删除表):\n%s\n%v", dropOutput, err)
		return
	}

	restoreCmd := exec.Command("psql", "-U", "postgres", "-d", "postgres", "-f", "pgbench_accounts_backup.sql")
	restoreOutput, err := restoreCmd.CombinedOutput()
	if err != nil {
		log.Printf("恢复错误: %v\n", err)
		fmt.Fprintf(w, "恢复失败:\n%s\n%v", restoreOutput, err)
		return
	}

	log.Println("恢复成功")
	fmt.Fprintf(w, "恢复成功:\n%s", restoreOutput)
}

func handleCompare(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }

    similarity, details := compareTablesWithHash("active_employees", "employees")
    
    response := fmt.Sprintf("表的相似度: %.2f%%\n\n计算过程:\n%s", similarity*100, details)
    fmt.Fprint(w, response)
}

func compareTablesWithHash(table1, table2 string) (float64, string) {
    var details string
    details += fmt.Sprintf("比较表 %s 和 %s:\n", table1, table2)

    var count1, count2 int
    err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table1)).Scan(&count1)
    if err != nil {
        log.Printf("获取 %s 表行数错误: %v\n", table1, err)
        return 0, details + fmt.Sprintf("获取 %s 表行数错误: %v\n", table1, err)
    }
    err = db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table2)).Scan(&count2)
    if err != nil {
        log.Printf("获取 %s 表行数错误: %v\n", table2, err)
        return 0, details + fmt.Sprintf("获取 %s 表行数错误: %v\n", table2, err)
    }

    details += fmt.Sprintf("%s 表行数: %d\n", table1, count1)
    details += fmt.Sprintf("%s 表行数: %d\n", table2, count2)

    columns1, err := getTableColumns(table1)
    if err != nil {
        log.Printf("获取 %s 表列名错误: %v\n", table1, err)
        return 0, details + fmt.Sprintf("获取 %s 表列名错误: %v\n", table1, err)
    }
    columns2, err := getTableColumns(table2)
    if err != nil {
        log.Printf("获取 %s 表列名错误: %v\n", table2, err)
        return 0, details + fmt.Sprintf("获取 %s 表列名错误: %v\n", table2, err)
    }

    if !areSlicesEqual(columns1, columns2) {
        details += "表结构不同,无法比较\n"
        return 0, details
    }

    hashes1, err := getTableHashes(table1, columns1)
    if err != nil {
        log.Printf("计算 %s 表哈希值错误: %v\n", table1, err)
        return 0, details + fmt.Sprintf("计算 %s 表哈希值错误: %v\n", table1, err)
    }
    hashes2, err := getTableHashes(table2, columns2)
    if err != nil {
        log.Printf("计算 %s 表哈希值错误: %v\n", table2, err)
        return 0, details + fmt.Sprintf("计算 %s 表哈希值错误: %v\n", table2, err)
    }

    matchCount := 0
    for hash := range hashes1 {
        if _, exists := hashes2[hash]; exists {
            matchCount++
        }
    }

    details += fmt.Sprintf("匹配的行数: %d\n", matchCount)

    similarity := float64(matchCount) / float64(max(len(hashes1), len(hashes2)))

    return similarity, details
}

func getTableColumns(tableName string) ([]string, error) {
    query := fmt.Sprintf("SELECT column_name FROM information_schema.columns WHERE table_name = '%s' ORDER BY ordinal_position", tableName)
    rows, err := db.Query(query)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var columns []string
    for rows.Next() {
        var column string
        if err := rows.Scan(&column); err != nil {
            return nil, err
        }
        columns = append(columns, column)
    }
    return columns, nil
}

func getTableHashes(tableName string, columns []string) (map[string]bool, error) {
    query := fmt.Sprintf("SELECT MD5(CONCAT(%s)) FROM %s", concatenateColumns(columns), tableName)
    rows, err := db.Query(query)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    hashes := make(map[string]bool)
    for rows.Next() {
        var hash string
        if err := rows.Scan(&hash); err != nil {
            return nil, err
        }
        hashes[hash] = true
    }
    return hashes, nil
}

func concatenateColumns(columns []string) string {
    result := "COALESCE(" + columns[0] + "::text, '')"
    for _, col := range columns[1:] {
        result += " || ',' || COALESCE(" + col + "::text, '')"
    }
    return result
}

func areSlicesEqual(a, b []string) bool {
    if len(a) != len(b) {
        return false
    }
    for i := range a {
        if a[i] != b[i] {
            return false
        }
    }
    return true
}

func max(a, b int) int {
    if a > b {
        return a
    }
    return b
}

func backgroundTasks() {
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				performRandomDatabaseOperation()
				time.Sleep(time.Duration(rand.Intn(1000)) * time.Millisecond)
			}
		}()
	}
	wg.Wait()
}

func performRandomDatabaseOperation() {
	switch rand.Intn(3) {
	case 0:
		// 插入操作
		_, err := db.Exec("INSERT INTO test_table (data) VALUES ($1)", randomString(10))
		if err != nil {
			log.Println("插入数据错误:", err)
		} else {
			log.Println("成功插入数据")
		}
	case 1:
		// 查询操作
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM test_table").Scan(&count)
		if err != nil {
			log.Println("查询数据错误:", err)
		} else {
			log.Printf("查询结果: 表中有 %d 行数据", count)
		}
	case 2:
		// 更新操作
		result, err := db.Exec("UPDATE test_table SET data = $1 WHERE id = (SELECT id FROM test_table ORDER BY RANDOM() LIMIT 1)", randomString(10))
		if err != nil {
			log.Println("更新数据错误:", err)
		} else {
			rowsAffected, _ := result.RowsAffected()
			log.Printf("成功更新 %d 行数据", rowsAffected)
		}
	}
}

func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}