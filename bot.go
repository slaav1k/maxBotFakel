package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	maxigo "github.com/maxigo-bot/maxigo-client"
)

var registeredUsers = make(map[int64]string) // UserID → телефон

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

				if base.UpdateType == maxigo.UpdateMessageCreated {
					var msgUpd maxigo.MessageCreatedUpdate
					if err := json.Unmarshal(rawUpd, &msgUpd); err != nil {
						continue
					}

					msg := msgUpd.Message
					chatID := *msg.Recipient.ChatID
					sender := msg.Sender
					userID := sender.UserID

					senderName := sender.FirstName
					if sender.LastName != nil {
						senderName += " " + *sender.LastName
					}

					var text string
					if msg.Body.Text != nil {
						text = *msg.Body.Text
					}
					if text == "" {
						text = "[без текста]"
					}

					fmt.Printf("Сообщение от %s (UserID %d) в чате %d: %s\n", senderName, userID, chatID, text)

					// Проверяем, зарегистрирован ли пользователь
					phone, isRegistered := registeredUsers[userID]
					registrationJustHappened := false // Флаг, что регистрация произошла в этом сообщении

					// Если пришёл контакт, то сохраняем/обновляем
					if len(msg.Body.Attachments) > 0 {
						parsed, parseErr := msg.Body.ParseAttachments()
						if parseErr != nil {
							fmt.Printf("Ошибка парсинга attachments: %v\n", parseErr)
						} else {
							for _, att := range parsed {
								if contact, ok := att.(*maxigo.ContactAttachment); ok {
									payload := contact.Payload

									phoneFromContact := payload.Phone()
									if phoneFromContact == "" {
										phoneFromContact = "[не удалось извлечь]"
									}

									// Сохраняем номер по UserID
									registeredUsers[userID] = phoneFromContact
									fmt.Printf("ПОЛУЧЕН КОНТАКТ! UserID=%d → Телефон: %q\n", userID, phoneFromContact)

									// Ответ пользователю
									_, err := client.SendMessage(ctx, chatID, &maxigo.NewMessageBody{
										Text: maxigo.Some(fmt.Sprintf("Спасибо! Номер %s сохранён ✨ Теперь ты зарегистрирован.", phoneFromContact)),
									})
									if err != nil {
										fmt.Printf("Ошибка отправки сообщения: %v\n", err)
									}

									// Устанавливаем флаг и обновляем переменные для текущей итерации
									registrationJustHappened = true
									phone = phoneFromContact
									isRegistered = true

									break // Выходим из цикла обработки вложений
								}
							}
						}
					}

					// ЕСЛИ ТОЛЬКО ЧТО ПРОИЗОШЛА РЕГИСТРАЦИЯ - НИЧЕГО БОЛЬШЕ НЕ ДЕЛАЕМ ДЛЯ ЭТОГО АПДЕЙТА
					if registrationJustHappened {
						continue // Переходим к следующему апдейту
					}

					// Логика для обычных сообщений
					if !isRegistered {
						// Игнорируем пустые сообщения (например, служебные)
						if text == "[без текста]" || text == "" {
							continue
						}

						// Не зарегистрирован → сразу предлагаем кнопку
						contactBtn := maxigo.NewRequestContactButton("📱 Регистрация")

						keyboard := maxigo.NewInlineKeyboardAttachment([][]maxigo.Button{
							{contactBtn},
						})

						_, err := client.SendMessage(ctx, chatID, &maxigo.NewMessageBody{
							Text:        maxigo.Some("Привет!\nЧтобы продолжить общение, пожалуйста, зарегистрируйся!"),
							Attachments: []maxigo.AttachmentRequest{keyboard},
						})
						if err != nil {
							fmt.Printf("Ошибка отправки сообщения: %v\n", err)
						}

						fmt.Printf("Предложена регистрация для UserID %d\n", userID)
						continue
					}

					// Зарегистрирован → обычный ответ
					_, err := client.SendMessage(ctx, chatID, &maxigo.NewMessageBody{
						Text: maxigo.Some(fmt.Sprintf("Привет, %s! ✨\nПолучил: «%s»\nТвой номер уже сохранён (%s)", senderName, text, phone)),
					})
					if err != nil {
						fmt.Printf("Ошибка отправки сообщения: %v\n", err)
					}
				}
			}

			if updates.Marker != nil {
				lastMarker = *updates.Marker
			}
		}
	}
}
