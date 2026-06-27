# Outlook / Microsoft Graph + IMAP 实现说明

## 认证方式演变

Microsoft 已于 2024-2026 年分阶段禁用 Basic Authentication（明文密码），
全面转向 OAuth2（Modern Authentication）。

### 关键时间线

| 事件 | 时间 | 说明 |
|------|------|------|
| IMAP/POP Basic Auth 禁用 | 2024 年 9 月 | 明文密码 / app password 登录 IMAP/POP 失效 |
| SMTP AUTH Basic Auth 开始禁用 | 2026 年 3 月 | 分阶段丢弃 |
| SMTP AUTH 100% 禁用 | 2026 年 4 月 30 日 | 发送也不可用 |

**App Password 本质上是 Basic Auth 的一种形式，已完全失效。**

### 当前可用的认证方式

只有 **OAuth2（XOAUTH2 SASL 机制）** 可以连接 Outlook/Office 365 的 IMAP 和 SMTP。

## IMAP + XOAUTH2 实现要点

1. 在 Azure AD 注册应用获取 `Client ID`
2. 用 **OAuth2 Device Code Flow** 获取 refresh token（适合 CLI/后台应用）
3. 用 refresh token 定期换取 access token
4. IMAP 连接使用 `XOAUTH2` SASL 认证（而非 `PLAIN`）
5. SMTP 同样需要 OAuth2 access token

### 相关资源

- [Microsoft OAuth Authentication and Thunderbird in 2026](https://support.mozilla.org/en-US/kb/microsoft-oauth-authentication-and-thunderbird-202)
- [Microsoft 官方 - Modern Authentication](https://learn.microsoft.com/en-us/exchange/clients-and-mobile-in-exchange-online/deprecation-of-basic-authentication-exchange-online)
- [Getmailbird - Microsoft Modern Authentication 2026 Guide](https://www.getmailbird.com/microsoft-modern-authentication-enforcement-email-guide/)

## 当前状态

- `outlookgraph/` — 使用 Microsoft Graph API（REST），不受 IMAP/SMTP 禁用影响 ✅
- `emailimap/` — 仍使用 IMAP + 明文密码 / app password，**需要改为 XOAUTH2**
