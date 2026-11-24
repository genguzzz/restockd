package engine

import (
	"fmt"

	"github.com/genguzzz/restockd/internal/domain"
)

// Diff 比较两次目录快照生成事件。prev 为空（首次抓取）时不产生 new_product / restock，避免启动时刷屏。
func Diff(siteID string, prev, next []domain.Product) []domain.Event {
	if len(prev) == 0 {
		return nil
	}
	previous := make(map[string]domain.Product, len(prev))
	for _, product := range prev {
		previous[product.ID] = product
	}
	var events []domain.Event
	for _, product := range next {
		old, existed := previous[product.ID]
		switch {
		case !existed:
			events = append(events, domain.Event{Type: domain.EventNewProduct, SiteID: siteID, ProductID: product.ID, Title: "新上架 " + product.Name, Detail: stockText(product.Stock)})
			if product.Stock.Status.Orderable() {
				events = append(events, restockEvent(siteID, product))
			}
		case !old.Stock.Status.Orderable() && product.Stock.Status.Orderable():
			events = append(events, restockEvent(siteID, product))
		case old.Stock.Status.Orderable() && !product.Stock.Status.Orderable():
			events = append(events, domain.Event{Type: domain.EventSoldOut, SiteID: siteID, ProductID: product.ID, Title: product.Name + " 已售罄"})
		}
		if existed {
			if detail, changed := priceChange(old, product); changed {
				events = append(events, domain.Event{Type: domain.EventPriceChange, SiteID: siteID, ProductID: product.ID, Title: product.Name + " 价格变动", Detail: detail})
			}
		}
	}
	return events
}

func restockEvent(siteID string, product domain.Product) domain.Event {
	return domain.Event{Type: domain.EventRestock, SiteID: siteID, ProductID: product.ID, Title: product.Name + " 补货" + qtySuffix(product.Stock), Detail: priceSummary(product)}
}

func qtySuffix(stock domain.Stock) string {
	if stock.Qty == nil {
		return ""
	}
	return fmt.Sprintf(" (%d 台)", *stock.Qty)
}

func stockText(stock domain.Stock) string {
	return string(stock.Status) + qtySuffix(stock)
}

func priceSummary(product domain.Product) string {
	if len(product.Prices) == 0 {
		return ""
	}
	price := product.Prices[0]
	for _, candidate := range product.Prices {
		if candidate.Cycle == domain.CycleMonthly {
			price = candidate
		}
	}
	return fmt.Sprintf("%s %s", price.Amount, price.Cycle)
}

// priceChange 同一周期、同一币种的金额变化。币种不同（如账号结算币种与匿名访问不同、商家切换显示币种）
// 无法比较，不算变动——否则每次切换都会把全部商品报成「价格变动」。
func priceChange(old, next domain.Product) (string, bool) {
	for _, price := range next.Prices {
		previous, ok := old.PriceFor(price.Cycle)
		if ok && previous.Amount.Currency == price.Amount.Currency && previous.Amount.Cents != price.Amount.Cents {
			return fmt.Sprintf("%s: %s → %s", price.Cycle, previous.Amount, price.Amount), true
		}
	}
	return "", false
}
