package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

type Config struct {
	DatabaseURL string
	RedisURL    string // Хранит адрес подключения к Redis
	Port        string
}

type GeneratedSequence struct {
	ID        int       `json:"id"`
	Chords    []string  `json:"chords"`
	CreatedAt time.Time `json:"created_at"`
}

var (
	// Полный пул медиаторных аккордов для режима абсолютного хаоса
	chordsEasy = []string{
		"Am", "Am7", "Am9", "Asus4", "C", "Cmaj7", "C7", "C9",
		"G", "G7", "Gsus4", "Em", "Em7", "Em9", "Esus4",
		"D", "Dm", "Dm7", "D7", "Dsus2", "Dsus4", "A", "A7", "Asus2",
	}

	chordsHard = []string{
		"F", "Fmaj7", "Fm", "Fm7", "F9", "B", "B7", "Bm", "Bm7",
		"C#", "C#m", "C#m7", "C#maj7", "G#", "G#m", "G#m7",
		"D#", "D#m", "D#m7", "F#", "F#m", "F#7", "H", "Hm", "H7",
	}

	// СУПЕР-СИНХРОНИЗИРОВАННАЯ КАРТА СТУПЕНЕЙ (Защита от багов регистров букв)
	harmonyKeys = map[string]map[string]string{
		"C": {
			"1": "Cmaj7", "2": "Dm7", "3": "Em7", "4": "Fmaj7", "5": "C7", "6": "Am7", "7": "Bdim",
			"1_sus": "Csus4", "5_alt": "C9",
			"borrowed": "Fm7",
		},
		"Am": {
			"1": "Am7", "2": "Bdim", "3": "Cmaj7", "4": "Dm7", "5": "E7", "6": "Fmaj7", "7": "G7",
			"1_sus": "Am7", "5_alt": "E7",
			"borrowed": "D9",
		},
		"G": {
			"1": "Gmaj7", "2": "Am7", "3": "Bm7", "4": "Cmaj7", "5": "D7", "6": "Em7", "7": "F#dim",
			"1_sus": "Gsus4", "5_alt": "D9",
			"borrowed": "Cm6",
		},
		"Em": {
			"1": "Em7", "2": "F#dim", "3": "Gmaj7", "4": "Am7", "5": "H7", "6": "Cmaj7", "7": "D7",
			"1_sus": "Em9", "5_alt": "H7",
			"borrowed": "A9",
		},
	}

	// НАСТРОЕНИЯ (Используют строгие цифровые индексы ступеней лада)
	moodProgressions = map[string][][]string{
		"happy": {
			{"1", "5", "6", "4"},         // Золотая поп-последовательность
			{"1", "1_sus", "4", "5_alt"}, // Инди-мажор с сусами и девятками
			{"1", "4", "6", "5"},         // Солнечный перебор
		},
		"sad": {
			{"1", "6", "3", "7"},     // Лирический минорный рок
			{"1", "4", "5_alt", "5"}, // Классическая романсовая драма
			{"1", "6", "4", "5"},     // Тягучая баллада
		},
		"epic": {
			{"1", "6", "7", "1"},        // Героическая (Em -> Cmaj7 -> D7 -> Em)
			{"6", "4", "1", "5"},        // Кинематографичный размах
			{"1", "borrowed", "6", "5"}, // Модуляция через заимствованную дорийскую ступень
		},
		"jazz": {
			{"2", "5_alt", "1", "6"}, // Легендарный ii-V-I джаз-стандарт
			{"1", "6", "2", "5_alt"}, // Американский джазовый турнаунд
			{"1", "4", "2", "5_alt"}, // Джаз-свинг медиатором
		},
	}
)

type Server struct {
	db  *pgxpool.Pool
	rdb *redis.Client // Клиент Redis встроен в структуру сервера
}

func main() {
	_ = godotenv.Load()

	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		RedisURL:    os.Getenv("REDIS_URL"),
		Port:        os.Getenv("PORT"),
	}
	if cfg.Port == "" {
		cfg.Port = "8085"
	}

	// 1. Инициализация пула PostgreSQL
	var dbPool *pgxpool.Pool
	var err error
	if cfg.DatabaseURL != "" {
		dbPool, err = pgxpool.New(context.Background(), cfg.DatabaseURL)
		if err != nil {
			log.Fatalf("Не удалось подключиться к БД: %v\n", err)
		}
		defer dbPool.Close()
		fmt.Println("Успешное подключение к PostgreSQL")
	}

	// 2. Инициализация клиента Redis
	var redisClient *redis.Client
	if cfg.RedisURL != "" {
		opt, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			log.Printf("Критическая ошибка разбора REDIS_URL: %v\n", err)
		} else {
			redisClient = redis.NewClient(opt)
			// Проверяем доступность Redis быстрым тестовым пингом
			if err := redisClient.Ping(context.Background()).Err(); err != nil {
				log.Printf("Предупреждение: Redis недоступен: %v\n", err)
			} else {
				fmt.Println("Успешное подключение к Redis Cache")
			}
		}
	}

	srv := &Server{db: dbPool, rdb: redisClient}

	r := gin.Default()

	// Раздача статических файлов фронтенда
	r.StaticFile("/app.js", "./app.js")
	r.StaticFile("/style.css", "./style.css")
	r.StaticFile("/index.html", "./index.html")

	r.GET("/", func(c *gin.Context) { c.File("index.html") })

	// Регистрация API Роутов
	r.GET("/api/chords/random", srv.handleRandomChords)
	r.POST("/api/chords/save", srv.handleSaveSequence)
	r.GET("/api/chords/history", srv.handleGetHistory)
	r.DELETE("/api/chords/delete", srv.handleDeleteSequence)

	fmt.Printf("Сервер запущен на http://127.0.0.1:%s\n", cfg.Port)
	log.Fatal(r.Run(":" + cfg.Port))

}

// Умная генерация последовательностей по законам музыкальной гармонии
func (srv *Server) handleRandomChords(c *gin.Context) {
	keyParam := strings.TrimSpace(c.Query("key"))
	difficulty := c.Query("level")
	mood := c.Query("mood")
	length, err := strconv.Atoi(c.Query("length"))
	if err != nil || length <= 0 {
		length = 4
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	var result []string

	if mood != "" && mood != "random" && keyParam != "" {
		if currentKey, ok := harmonyKeys[keyParam]; ok {
			if progressions, ok := moodProgressions[mood]; ok {
				chosenProg := progressions[rng.Intn(len(progressions))]

				for i := 0; i < length; i++ {
					step := chosenProg[i%len(chosenProg)]
					chord := currentKey[step]

					// Шанс 15% разбавить цепочку заимствованным аккордом
					if rng.Float32() < 0.15 && currentKey["borrowed"] != "" && i > 0 {
						chord = currentKey["borrowed"]
					}

					// Фильтр сложности Easy (убираем баррэ)
					if difficulty == "easy" && (chord == "F" || chord == "Fm" || chord == "B" || chord == "Bm" || chord == "C#m" || chord == "G#m" || chord == "D#m" || chord == "F#") {
						chord = "Am"
					}
					result = append(result, chord)
				}
			}
		}
	}

	// Абсолютный хаос (фолбек на рандом)
	if len(result) == 0 {
		var pool []string
		pool = append(pool, chordsEasy...)
		if difficulty == "hard" {
			pool = append(pool, chordsHard...)
		}

		for i := 0; i < length; i++ {
			chord := pool[rng.Intn(len(pool))]
			if rng.Float32() < 0.12 && i == length-1 {
				chord = "E7"
			}
			result = append(result, chord)
		}
	}

	c.JSON(http.StatusOK, gin.H{"sequence": result})
}

// Сохранение цепочки аккордов в PostgreSQL с автоматическим сбросом кэша в Redis
func (srv *Server) handleSaveSequence(c *gin.Context) {
	if srv == nil || srv.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "БД недоступна"})
		return
	}
	var req struct {
		Chords []string `json:"chords"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Chords) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Пустая последовательность"})
		return
	}

	chordsJSON, _ := json.Marshal(req.Chords)
	ctx := c.Request.Context()
	var artistID int
	errArtist := srv.db.QueryRow(ctx,
		`INSERT INTO artists (name) VALUES ($1) 
		 ON CONFLICT (name) DO UPDATE SET name=EXCLUDED.name 
		 RETURNING id`, "Генератор").Scan(&artistID)

	if errArtist != nil {
		_ = srv.db.QueryRow(ctx, `SELECT id FROM artists WHERE name = $1 LIMIT 1`, "Генератор").Scan(&artistID)
		if artistID == 0 {
			_ = srv.db.QueryRow(ctx, `INSERT INTO artists (name) VALUES ($1) RETURNING id`, "Генератор").Scan(&artistID)
		}
	}

	title := fmt.Sprintf("Тренировка от %s", time.Now().Format("02.01 15:04"))
	_, errSong := srv.db.Exec(ctx,
		`INSERT INTO songs (artist_id, title, content) VALUES ($1, $2, $3)`,
		artistID, title, string(chordsJSON))

	if errSong != nil {
		log.Printf("[БД ОШИБКА] Не удалось записать в songs: %v\n", errSong)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка записи в таблицу songs"})
		return
	}

	if srv.rdb != nil {
		srv.rdb.Del(ctx, "chord_history_cache")
	}

	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

// Получение истории
func (srv *Server) handleGetHistory(c *gin.Context) {
	if srv == nil {
		return
	}
	ctx := c.Request.Context()

	if srv.rdb != nil {
		cachedData, err := srv.rdb.Get(ctx, "chord_history_cache").Result()
		if err == nil && cachedData != "" {
			c.Data(http.StatusOK, "application/json", []byte(cachedData))
			return
		}
	}

	if srv.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "БД недоступна"})
		return
	}

	sql := `SELECT s.id, s.content FROM songs s 
	        JOIN artists a ON s.artist_id = a.id 
	        WHERE a.name = 'Генератор' 
	        ORDER BY s.id DESC LIMIT 10`

	rows, err := srv.db.Query(ctx, sql)
	if err != nil {
		log.Printf("[БД ОШИБКА] Ошибка чтения истории: %v\n", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка чтения истории из базы"})
		return
	}
	defer rows.Close()

	type FrontSequence struct {
		ID     int      `json:"id"`
		Chords []string `json:"chords"`
	}

	var history []FrontSequence
	for rows.Next() {
		var id int
		var contentStr string
		if err := rows.Scan(&id, &contentStr); err == nil {
			var ch []string
			if err := json.Unmarshal([]byte(contentStr), &ch); err == nil {
				history = append(history, FrontSequence{ID: id, Chords: ch})
			}
		}
	}

	if len(history) > 0 && srv.rdb != nil {
		jsonBytes, errMarshal := json.Marshal(history)
		if errMarshal == nil {
			srv.rdb.Set(ctx, "chord_history_cache", string(jsonBytes), 5*time.Minute)
		}
	}

	c.JSON(http.StatusOK, history)
}

// Удаление цепочки аккордов (ИСПРАВЛЕНО!)
func (srv *Server) handleDeleteSequence(c *gin.Context) {
	if srv == nil || srv.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "БД недоступна"})
		return
	}

	idParam := c.Query("id")
	if idParam == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Не указан ID для удаления"})
		return
	}

	songID, err := strconv.Atoi(idParam)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Некорректный ID"})
		return
	}

	ctx := c.Request.Context()

	// Из строки ниже убран ломающий слэш перед \$1
	_, errDelete := srv.db.Exec(ctx, `DELETE FROM songs WHERE id = $1`, songID)
	if errDelete != nil {
		log.Printf("[БД ОШИБКА] Не удалось удалить запись ID=%d: %v\n", songID, errDelete)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Ошибка удаления записи из базы данных"})
		return
	}

	if srv.rdb != nil {
		srv.rdb.Del(ctx, "chord_history_cache")
	}

	log.Printf("[БД УСПЕХ] Цепочка аккордов ID=%d успешно удалена.\n", songID)
	c.JSON(http.StatusOK, gin.H{"status": "success", "message": "Запись успешно удалена"})
}
