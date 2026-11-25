package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/genguzzz/restockd/internal/domain"
	"github.com/genguzzz/restockd/internal/store"
)

// stockWindowRetention 已结束在售期的保留期（补货规律要看得比事件更久）。
const stockWindowRetention = 365 * 24 * time.Hour

// trackStock 按本次目录更新在售期；失败只记日志（不影响事件与自动锁单）。
func (e *Engine) trackStock(ctx context.Context, siteID string, products []domain.Product, changes []domain.Event) store.StockChanges {
	restocked := map[string]bool{}
	for _, change := range changes {
		if change.Type == domain.EventRestock {
			restocked[change.ProductID] = true
		}
	}
	history, err := e.store.TrackStock(ctx, siteID, products, restocked, time.Now())
	if err != nil {
		e.log.Warn("track stock", "site", siteID, "err", err)
	}
	return history
}

// withStockHistory 给补货 / 售罄事件补上历史：售罄写本次在售时长，补货写上次补货距今与当时在售多久。
func withStockHistory(event domain.Event, history store.StockChanges, now time.Time) string {
	var note string
	switch event.Type {
	case domain.EventSoldOut:
		if window, ok := history.Closed[event.ProductID]; ok {
			note = "在售 " + formatSpan(window.Duration(now))
			if window.Baseline {
				note = "在售至少 " + formatSpan(window.Duration(now))
			}
		}
	case domain.EventRestock:
		if window, ok := history.Previous[event.ProductID]; ok {
			note = fmt.Sprintf("上次补货 %s前，在售 %s", formatSpan(now.Sub(window.StartedAt)), formatSpan(window.Duration(now)))
		}
	}
	return joinNonEmpty(" · ", event.Detail, note)
}

// formatSpan 人读时长：秒 / 分钟 / 小时 / 天，取两级精度（如「2 小时 5 分钟」）。
func formatSpan(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return joinNonEmpty(" ", fmt.Sprintf("%d 小时", int(d.Hours())), nonZero(int(d.Minutes())%60, "分钟"))
	default:
		return joinNonEmpty(" ", fmt.Sprintf("%d 天", int(d.Hours())/24), nonZero(int(d.Hours())%24, "小时"))
	}
}

func nonZero(value int, unit string) string {
	if value == 0 {
		return ""
	}
	return fmt.Sprintf("%d %s", value, unit)
}

func joinNonEmpty(sep string, parts ...string) string {
	kept := parts[:0:0]
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, sep)
}
