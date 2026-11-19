package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

const groupPanelText = "🚀 <b>Новые точки роста для Вашего бизнеса</b>\n\nНайдите перспективную локацию для развития Вашего бизнеса. Изучайте предложения, сравнивайте варианты и выбирайте новые точки роста!"

func groupPanelKeyboard() map[string]any {
	return markup([]Button{callbackButton("📍 Позвать помощника", "grp:menu")})
}

func (b *Bot) loadGroupPanelsLocked() error {
	if b.groupPanelIDs != nil {
		return nil
	}
	b.groupPanelIDs = map[int64]int{}
	if b.groupPanelPath == "" {
		return nil // Test/in-memory bot.
	}
	raw, err := os.ReadFile(b.groupPanelPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read group panel metadata: %w", err)
	}
	var entries map[string]int
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("decode group panel metadata: %w", err)
	}
	for key, id := range entries {
		chatID, err := strconv.ParseInt(key, 10, 64)
		if err == nil && id > 0 {
			b.groupPanelIDs[chatID] = id
		}
	}
	return nil
}

func (b *Bot) saveGroupPanelsLocked() error {
	if b.groupPanelPath == "" {
		return nil
	}
	entries := make(map[string]int, len(b.groupPanelIDs))
	for chatID, id := range b.groupPanelIDs {
		if id > 0 {
			entries[strconv.FormatInt(chatID,10)] = id
		}
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	return atomicAdminFile(b.groupPanelPath, raw)
}

func groupPanelMessageID(raw json.RawMessage) (int,error) {
	var v struct { MessageID int `json:"message_id"` }
	if err:=json.Unmarshal(raw,&v); err!=nil {return 0,err}
	if v.MessageID<=0 {return 0,errors.New("telegram did not return a message_id")}
	return v.MessageID,nil
}

// On the first upgrade, reuse the previously pinned panel when possible.
// A different pinned announcement must never be edited by this code.
func (b *Bot) findExistingPinnedPanel(ctx context.Context, chatID int64) int {
	raw,err:=b.call(ctx,"getChat",map[string]any{"chat_id":chatID})
	if err!=nil {return 0}
	var result struct {
		PinnedMessage struct {
			MessageID int `json:"message_id"`
			ReplyMarkup struct {
				InlineKeyboard [][]struct { CallbackData string `json:"callback_data"` } `json:"inline_keyboard"`
			} `json:"reply_markup"`
		} `json:"pinned_message"`
	}
	if json.Unmarshal(raw,&result)!=nil{return 0}
	for _,row:=range result.PinnedMessage.ReplyMarkup.InlineKeyboard {
		for _,button:=range row {
			if button.CallbackData=="grp:menu" {return result.PinnedMessage.MessageID}
		}
	}
	return 0
}

func groupPanelIsGone(err error) bool {
	if err==nil{return false}
	message:=strings.ToLower(err.Error())
	return strings.Contains(message,"message to edit not found") ||
		strings.Contains(message,"message_id_invalid") ||
		strings.Contains(message,"message can't be edited") ||
		strings.Contains(message,"message cannot be edited") ||
		strings.Contains(message,"message not found")
}

func (b *Bot) publishAndPinGroupPanel(ctx context.Context, chatID int64) error {
	if chatID>=0{return errors.New("assistant panel requires a group chat")}
	b.groupPanelMu.Lock()
	defer b.groupPanelMu.Unlock()
	if err:=b.loadGroupPanelsLocked();err!=nil{return err}
	messageID:=b.groupPanelIDs[chatID]
	if messageID==0 {
		messageID=b.findExistingPinnedPanel(ctx,chatID)
	}
	if messageID>0 {
		_,err:=b.call(ctx,"editMessageText",map[string]any{
			"chat_id":chatID,"message_id":messageID,
			"text":groupPanelText,"parse_mode":"HTML",
			"reply_markup":groupPanelKeyboard(),
			"link_preview_options":map[string]any{"is_disabled":true},
		})
		if err!=nil && !strings.Contains(strings.ToLower(err.Error()),"message is not modified") {
			if !groupPanelIsGone(err){return fmt.Errorf("update group assistant panel: %w",err)}
			log.Printf("Group panel %d no longer available in %d; creating replacement",messageID,chatID)
			messageID=0
		}
	}
	if messageID==0 {
		raw,err:=b.call(ctx,"sendMessage",map[string]any{
			"chat_id":chatID,"text":groupPanelText,"parse_mode":"HTML",
			"reply_markup":groupPanelKeyboard(),
			"link_preview_options":map[string]any{"is_disabled":true},
		})
		if err!=nil{return fmt.Errorf("publish group assistant panel: %w",err)}
		messageID,err=groupPanelMessageID(raw)
		if err!=nil{return err}
	}
	if b.groupPanelIDs[chatID]!=messageID {
		previous:=b.groupPanelIDs[chatID]
		b.groupPanelIDs[chatID]=messageID
		if err:=b.saveGroupPanelsLocked();err!=nil {
			b.groupPanelIDs[chatID]=previous
			return fmt.Errorf("save group assistant panel ID: %w",err)
		}
	}
	_,err:=b.call(ctx,"pinChatMessage",map[string]any{
		"chat_id":chatID,
		"message_id":messageID,
		"disable_notification":true,
	})
	if err!=nil {
		if strings.Contains(strings.ToLower(err.Error()),"already pinned") {
			return nil
		}
		return fmt.Errorf("pin group assistant panel (check can_pin_messages): %w",err)
	}
	return nil
}
