package telegram

// getCheburMenuText renders the Cheburcheck submenu header: a one-line
// description of the active RF probing, the /cheburcheck usage hint, an
// empty-state note when no enabled proxy exists (Wave-F B4) and — when
// neither the CHEBURCHECK_API_KEY nor per-scanner tokens are configured — an
// explicit unconfigured notice. Static RU text only (no external strings are
// interpolated), so no escapeHTML call sites are needed.
func (b *Bot) getCheburMenuText(enabledProxies int) string {
	text := "🇷🇺 <b>Активный пробинг блокировок РФ (Cheburcheck)</b>\n\n" +
		"Сканеры из России отправляют реальные пробы к хосту и показывают тип блокировки " +
		"(SNI, ТСПУ, подмена DNS, CDN).\n\n" +
		"Выберите прокси из списка ниже или отправьте команду с любым хостом:\n" +
		"<code>/cheburcheck &lt;хост&gt;</code>"
	if enabledProxies == 0 {
		text += "\n\n📭 Нет активных прокси для проверки."
	}
	if !b.resolveCheburClient().HasCredentials() {
		text += "\n\n⚠️ Сканеры не настроены (CHEBURCHECK_API_KEY пуст)"
	}
	return text
}
