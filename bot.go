package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	maxigo "github.com/maxigo-bot/maxigo-client"
)

var registeredUsers = make(map[int64]string) // chatID → телефон

func main() {
	token := os.Getenv("MAXBOT_TOKEN")
	if token == "" {
		log.Fatal("MAXBOT_TOKEN не найден в переменных окружения")
	}

	client, err := maxigo.New(token)
	if err != nil {
		log.Fatal(err)
	}

	bot, err := client.GetBot(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Бот запущен: %s (ID: %d)\n", bot.FirstName, bot.UserID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		fmt.Println("\nПолучен сигнал завершения...")
		cancel()
	}()

	fmt.Println("Ожидаю сообщений... (Ctrl+C для выхода)")

	var lastMarker int64 = 0

	for {
		select {
		case <-ctx.Done():
			fmt.Println("Поллинг остановлен.")
			return
		default:
			updates, err := client.GetUpdates(ctx, maxigo.GetUpdatesOpts{
				Timeout: 30,
				Marker:  lastMarker,
				Limit:   100,
			})
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				fmt.Printf("Ошибка GetUpdates: %v\n", err)
				time.Sleep(5 * time.Second)
				continue
			}

			if len(updates.Updates) == 0 {
				continue
			}

			for _, rawUpd := range updates.Updates {
				var base maxigo.Update
				if err := json.Unmarshal(rawUpd, &base); err != nil {
					continue
				}

				switch base.UpdateType {
				case maxigo.UpdateMessageCreated:
					handleMessageCreated(client, ctx, rawUpd)
				case maxigo.UpdateMessageCallback:
					handleCallback(client, ctx, rawUpd)
				}
			}

			if updates.Marker != nil {
				lastMarker = *updates.Marker
			}
		}
	}
}

// ======================= ОБРАБОТКА ОБЫЧНЫХ СООБЩЕНИЙ =======================
func handleMessageCreated(client *maxigo.Client, ctx context.Context, raw []byte) {
	var upd maxigo.MessageCreatedUpdate
	if err := json.Unmarshal(raw, &upd); err != nil {
		return
	}

	msg := upd.Message
	chatID := *msg.Recipient.ChatID
	userID := msg.Sender.UserID

	senderName := msg.Sender.FirstName
	if msg.Sender.LastName != nil {
		senderName += " " + *msg.Sender.LastName
	}

	var text string
	if msg.Body.Text != nil {
		text = *msg.Body.Text
	}
	if text == "" {
		text = "[без текста]"
	}

	fmt.Printf("Сообщение от %s (UserID %d): %s\n", senderName, userID, text)

	phone, isRegistered := registeredUsers[userID]
	registrationJustHappened := false

	// Обработка контакта
	if len(msg.Body.Attachments) > 0 {
		if handleContact(client, ctx, chatID, userID, msg) {
			registrationJustHappened = true
		}
	}

	if registrationJustHappened {
		return
	}

	// Показываем нужное меню
	if !isRegistered {
		sendRegistrationMenu(client, ctx, chatID)
	} else {
		sendRegisteredMenu(client, ctx, chatID)
		fmt.Printf("Сообщение от %s (UserID %d) с телефоном %s: %s\n", senderName, userID, phone, text)
	}
}

// ======================= ОБРАБОТКА CALLBACK КНОПОК =======================
func handleCallback(client *maxigo.Client, ctx context.Context, raw []byte) {
	var cb maxigo.MessageCallbackUpdate
	if err := json.Unmarshal(raw, &cb); err != nil {
		fmt.Printf("Ошибка парсинга callback: %v\n", err)
		return
	}

	chatID := *cb.Message.Recipient.ChatID // ← по chatID
	// userID := cb.Message.Sender.UserID    // этот больше не нужен

	data := cb.Callback.Payload

	fmt.Printf("✅ Нажата кнопка: %s в чате %d\n", data, chatID)

	switch data {
	case "status":
		handleStatus(client, ctx, chatID)
	case "linked":
		handleLinkedPhones(client, ctx, chatID)
	case "contacts":
		sendContacts(client, ctx, chatID)
	}
}

// ======================= МЕНЮ =======================
func sendRegistrationMenu(client *maxigo.Client, ctx context.Context, chatID int64) {
	btn := maxigo.NewRequestContactButton("📱 Регистрация")

	keyboard := maxigo.NewInlineKeyboardAttachment([][]maxigo.Button{{btn}})

	_, _ = client.SendMessage(ctx, chatID, &maxigo.NewMessageBody{
		Text:        maxigo.Some("Привет!\nЧтобы продолжить, пожалуйста, зарегистрируйся"),
		Attachments: []maxigo.AttachmentRequest{keyboard},
	})
}

func sendRegisteredMenu(client *maxigo.Client, ctx context.Context, chatID int64) {
	btnStatus := maxigo.NewCallbackButton("Узнать свой статус", "status")
	btnLinked := maxigo.NewCallbackButton("Привязанные номера к гаражу", "linked")
	btnContacts := maxigo.NewCallbackButton("Контакты", "contacts")

	keyboard := maxigo.NewInlineKeyboardAttachment([][]maxigo.Button{
		{btnStatus},
		{btnLinked},
		{btnContacts},
	})

	_, _ = client.SendMessage(ctx, chatID, &maxigo.NewMessageBody{
		Text:        maxigo.Some("👋 Выберите действие:"),
		Attachments: []maxigo.AttachmentRequest{keyboard},
	})
}

// ======================= ОБРАБОТКА КОНТАКТА =======================
func handleContact(client *maxigo.Client, ctx context.Context, chatID, userID int64, msg maxigo.Message) bool {
	parsed, err := msg.Body.ParseAttachments()
	if err != nil {
		return false
	}

	for _, att := range parsed {
		if contact, ok := att.(*maxigo.ContactAttachment); ok {
			phone := contact.Payload.Phone()
			if phone == "" {
				phone = "[не удалось извлечь]"
			}

			registeredUsers[chatID] = phone // ← сохраняем по chatID !!!
			fmt.Printf("✅ Зарегистрирован chatID=%d → Телефон: %s\n", chatID, phone)

			_, _ = client.SendMessage(ctx, chatID, &maxigo.NewMessageBody{
				Text: maxigo.Some(fmt.Sprintf("Спасибо! Номер %s сохранён ✨", phone)),
			})

			sendRegisteredMenu(client, ctx, chatID)
			return true
		}
	}
	return false
}

// ======================= УЗНАТЬ СВОЙ СТАТУС =======================
func handleStatus(client *maxigo.Client, ctx context.Context, chatID int64) {
	phone, ok := registeredUsers[chatID]
	fmt.Printf("chatID=%d → Телефон: %s\n", chatID, phone)

	if !ok || phone == "" {
		_, _ = client.SendMessage(ctx, chatID, &maxigo.NewMessageBody{
			Text: maxigo.Some("Сначала зарегистрируйтесь!"),
		})
		return
	}

	// Здесь будет твоя логика поиска по CSV
	boxes := getLSBoxes(phone)
	if len(boxes) == 0 {
		_, _ = client.SendMessage(ctx, chatID, &maxigo.NewMessageBody{
			Text: maxigo.Some("Номер телефона не найден в базе.\nОбратитесь к администрации."),
		})
		return
	}

	for _, box := range boxes {
		dataList := readRowData(box)
		if len(dataList) == 0 {
			continue
		}

		data := dataList[0]
		formattedText := formatGarageData(data)

		_, _ = client.SendMessage(ctx, chatID, &maxigo.NewMessageBody{
			Text: maxigo.Some(formattedText),
		})
	}
}

// ======================= ПРИВЯЗАННЫЕ НОМЕРА =======================
func handleLinkedPhones(client *maxigo.Client, ctx context.Context, chatID int64) {
	phone, ok := registeredUsers[chatID]
	if !ok || phone == "" {
		_, _ = client.SendMessage(ctx, chatID, &maxigo.NewMessageBody{
			Text: maxigo.Some("Сначала зарегистрируйтесь"),
		})
		return
	}

	boxes := getLSBoxes(phone)
	if len(boxes) == 0 {
		_, _ = client.SendMessage(ctx, chatID, &maxigo.NewMessageBody{Text: maxigo.Some("Ничего не найдено")})
		return
	}

	for _, box := range boxes {
		// Ищем строку с этим гаражом в numbers.csv
		result := searchInNumbers(box)
		_, _ = client.SendMessage(ctx, chatID, &maxigo.NewMessageBody{
			Text: maxigo.Some(fmt.Sprintf("Гараж № %s:\n%s", box, result)),
		})
	}
}

// ======================= КОНТАКТЫ =======================
func sendContacts(client *maxigo.Client, ctx context.Context, chatID int64) {
	text := `ГСК ФАКЕЛ
Рязань, Южный Промузел, 19

Председатель: Архипкин Михаил Вячеславович
Тел: +79109061411, +79511013775

Администрация бота: @slaav1k`

	_, _ = client.SendMessage(ctx, chatID, &maxigo.NewMessageBody{Text: maxigo.Some(text)})
}

// ======================= ЗАГЛУШКИ (нужно будет реализовать) =======================
// ======================= РАБОТА С CSV ФАЙЛАМИ =======================

// getLSBoxes возвращает список номеров гаражей по номеру телефона
func getLSBoxes(phone string) []string {
	// Очищаем номер от лишних символов
	phone = strings.TrimPrefix(phone, "+")
	phone = strings.ReplaceAll(phone, " ", "")
	phone = strings.ReplaceAll(phone, "-", "")

	file, err := os.Open("numbers.csv")
	if err != nil {
		log.Printf("Ошибка открытия numbers.csv: %v", err)
		return nil
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.Comma = ';'
	reader.FieldsPerRecord = -1 // разрешаем разное количество полей

	rows, err := reader.ReadAll()
	if err != nil {
		log.Printf("Ошибка чтения numbers.csv: %v", err)
		return nil
	}

	var found []string
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		for _, cell := range row {
			// Ищем номер телефона в ячейке
			if strings.Contains(cell, phone) {
				// Берем первый столбец (номер гаража)
				garageNum := strings.TrimSpace(row[0])
				if garageNum != "" {
					found = append(found, garageNum)
				}
				break // переходим к следующей строке
			}
		}
	}

	// Убираем дубликаты
	unique := make(map[string]bool)
	var result []string
	for _, g := range found {
		if !unique[g] {
			unique[g] = true
			result = append(result, g)
		}
	}

	log.Printf("По номеру %s найдены гаражи: %v", phone, result)
	return result
}

// readRowData универсальный поиск по example.csv
// - по номеру гаража (точное совпадение) → возвращает 1 результат
// - по части ФИО → возвращает все совпадения
func readRowData(searchQuery string) []map[string]string {
	return readRowDataWithFile(searchQuery, "example.csv")
}

func readRowDataWithFile(searchQuery, filename string) []map[string]string {
	file, err := os.Open(filename)
	if err != nil {
		log.Printf("Ошибка открытия %s: %v", filename, err)
		return nil
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.Comma = ';'
	reader.FieldsPerRecord = -1

	rows, err := reader.ReadAll()
	if err != nil {
		log.Printf("Ошибка чтения %s: %v", filename, err)
		return nil
	}

	if len(rows) == 0 {
		log.Printf("Файл %s пуст", filename)
		return nil
	}

	// Заголовки
	headers := make([]string, len(rows[0]))
	for i, h := range rows[0] {
		headers[i] = strings.TrimSpace(h)
	}
	log.Printf("📋 Заголовки (%d): %v", len(headers), headers)

	searchLower := strings.ToLower(strings.TrimSpace(searchQuery))
	searchClean := strings.TrimRight(searchQuery, ".") // убираем точку в конце

	var results []map[string]string

	for i := 1; i < len(rows); i++ {
		row := rows[i]
		if len(row) == 0 {
			continue
		}

		// Очищаем ячейки
		cleanRow := make([]string, len(headers))
		for j := 0; j < len(headers); j++ {
			if j < len(row) {
				cleanRow[j] = strings.TrimSpace(row[j])
			} else {
				cleanRow[j] = ""
			}
		}

		// 1. Поиск по номеру гаража (первая колонка) — точное совпадение
		if len(cleanRow) > 0 && cleanRow[0] != "" {
			garageNum := strings.TrimRight(cleanRow[0], ".")
			if searchClean == garageNum {
				result := make(map[string]string)
				for j, header := range headers {
					result[header] = cleanRow[j]
				}
				log.Printf("✅ Найден по номеру гаража '%s' (строка %d)", searchQuery, i+1)
				return []map[string]string{result}
			}
		}

		// 2. Поиск по ФИО (вторая колонка) — частичное совпадение
		if len(cleanRow) > 1 && cleanRow[1] != "" {
			fio := strings.ToLower(cleanRow[1])
			if strings.Contains(fio, searchLower) {
				result := make(map[string]string)
				for j, header := range headers {
					result[header] = cleanRow[j]
				}
				results = append(results, result)
				log.Printf("✅ Найден по ФИО '%s' → %s (строка %d)", searchQuery, cleanRow[1], i+1)
			}
		}
	}

	if len(results) > 0 {
		log.Printf("✅ Найдено %d совпадений по запросу '%s'", len(results), searchQuery)
	} else {
		log.Printf("⚠️ По запросу '%s' ничего не найдено", searchQuery)
	}

	return results
}

// searchInNumbers ищет строку в numbers.csv и возвращает отформатированный результат
func searchInNumbers(searchQuery string) string {
	file, err := os.Open("numbers.csv")
	if err != nil {
		log.Printf("Ошибка открытия numbers.csv: %v", err)
		return "Ошибка открытия файла"
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.Comma = ';'
	reader.FieldsPerRecord = -1

	rows, err := reader.ReadAll()
	if err != nil {
		log.Printf("Ошибка чтения numbers.csv: %v", err)
		return "Ошибка чтения файла"
	}

	if len(rows) == 0 {
		return "Файл пуст"
	}

	// Ищем во всех строках
	for i, row := range rows {
		for _, cell := range row {
			if strings.Contains(cell, searchQuery) {
				// Форматируем вывод
				if i == 0 {
					// Заголовок
					var sb strings.Builder
					for _, h := range row {
						sb.WriteString(strings.TrimSpace(h) + " | ")
					}
					return sb.String()
				} else {
					// Данные
					var sb strings.Builder
					for _, v := range row {
						sb.WriteString(strings.TrimSpace(v) + " | ")
					}
					return sb.String()
				}
			}
		}
	}

	return "Не найдено"
}

// formatGarageData форматирует данные гаража для вывода
func formatGarageData(data map[string]string) string {
	// Желаемый порядок полей (только те, которые важны)
	order := []string{
		"№",
		"Гараж",
		"ФИО",
		"Должник",
		"Должен за 2026",
		"Должен за 2025",
		"Должен за 2024",
		"Должен за 2023",
		"Должен за 2022",
		"Должен за 2021",
		"Оплатил в 2026",
		"Оплатил 2026",
		"Дата оплаты",
		"Остаток долга",
		"Показания счетчиков",
		"Новые показания счетчиков",
		"Нажгли на",
		"Адрес",
		"Номер телефона",
		"Примечание",
	}

	var sb strings.Builder
	for _, key := range order {
		if val, ok := data[key]; ok && val != "" && val != "—" {
			// Очищаем ключ от возможных скрытых символов
			cleanKey := strings.TrimSpace(key)
			// Убираем точку в конце номера гаража
			if cleanKey == "№" && strings.HasSuffix(val, ".") {
				val = strings.TrimSuffix(val, ".")
			}
			sb.WriteString(fmt.Sprintf("%s: %s\n", cleanKey, val))
		}
	}

	// Если ничего не вывели, показываем всё, что есть
	if sb.Len() == 0 {
		for k, v := range data {
			if v != "" && v != "0" {
				sb.WriteString(fmt.Sprintf("%s: %s\n", k, v))
			}
		}
	}

	return sb.String()
}
