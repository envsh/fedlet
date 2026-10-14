#!/usr/bin/env bash
# mozpush-reguaid.sh — 一次性向 Mozilla push 预注册 UAID。
# channelID 由名字确定性派生(md5(name) → 小写 dashed UUID,与 fedbridge 一致)。
#
# 用法:
#   ./mozpush-reguaid.sh <name> [reg-base]
#     name     可读账号名(与 fedbridge -mobsub-name 保持一致)
#     reg-base 注册服务基址,默认 https://updates.push.services.mozilla.com
#
# 输出: stdout 为注册 JSON {uaid, channelID, endpoint, secret};
#       派生值另打印到 stderr。拿到 uaid/endpoint/secret 后即可喂给
#       ~/.config/fedlet/mobsub-<name>.json(配合程序生成的 ec_private/auth)。
#
# 关于 reg-base 里的 "updates" 主机(Mozilla push 分两套独立主机):
#   - 连接(WebSocket,Firefox 连这里): wss://push.services.mozilla.com/
#   - 端点(HTTP,发送方 POST 通知):   https://updates.push.services.mozilla.com/
#   本脚本 reg-base 用"端点"那台,生成的 endpoint URL 即以其为前缀。
#   两套主机在 autopush 中各自独立可配:端点=endpoint_scheme/endpoint_hostname
#   (默认 http://localhost),连接/路由=hostname/router_hostname。
#   "updates." 仅 Mozilla 部署命名(dev=updates-autopush.dev.mozaws.net、
#   stage=updates-push.services.mozaws.net、prod=updates.push.services.mozilla.com),
#   属基础设施命名,非协议要求。
#   发 VAPID 通知时 aud 必须等于端点 origin(即含 updates. 的那个主机)。
#
# 注册请求体只带 token/channelID(+aps=name;aps 仅透传,webpush router 直接忽略)。
# 不传 key ⇒ endpoint 为 v1 = 普通 capability URL:知道该 URL 即可推送,VAPID 可选。
# (若传 key=VAPID 公钥 ⇒ endpoint 变 v2,内嵌 sha256(key),发送时必须用匹配的
#  VAPID 私钥签 JWT,否则 MissingKey/KeyMismatch;故本脚本不传 key。)
#
# endpoint 如何交给"发送方"供推送(WebPush 规范做法,无服务端回调):
#   Mozilla/autopush 不会把 endpoint 或其变更主动通知应用服务器;按规范须由订阅方
#   经"应用自定义方式"(application-specific method,如发送方自己的订阅 API)交给
#   发送方。端点失效时,发送方只有在发送收到 404/410 才得知。
#   故请把本脚本输出的 endpoint 带外交给发送方;失效后重新注册并带外更新。
#
# 回收机制(fedlet 提示,非本脚本自动处理):
#   Mozilla push 的 UAID(router)记录有 TTL(autopush MAX_ROUTER_TTL_SECS=60 天),
#   通知最多存 30 天;离线过久积压超过 msg_limit(默认 150)会触发 user reset 直接换 UAID。
#   定期(建议每月)运行本脚本或让 fedbridge 正常 hello 即会刷新记录、避免被回收;
#   若已被回收:探测会得 410(NoUser)或 401(secret 失效),重跑本脚本注册即得新 UAID+endpoint,
#   旧 endpoint 失效,需重新告知发送方。
set -euo pipefail

name=${1:-}
if [[ -z $name ]]; then
	echo "usage: $0 <name> [reg-base]" >&2
	exit 2
fi
base=${2:-https://updates.push.services.mozilla.com}

command -v curl >/dev/null || {
	echo "need curl" >&2
	exit 1
}
command -v md5sum >/dev/null || {
	echo "need md5sum" >&2
	exit 1
}

# channelID = md5(name),原生 16 字节,手工格式化为 8-4-4-4-12
hex=$(printf '%s' "$name" | md5sum | cut -d' ' -f1)
chid="${hex:0:8}-${hex:8:4}-${hex:12:4}-${hex:16:4}-${hex:20:12}"

echo "name      = $name" >&2
echo "channelID = $chid" >&2
echo "reg-base  = $base" >&2

curl -fsS -X POST "$base/v1/webpush/$name/registration" \
	-H 'Content-Type: application/json' \
	--data-binary "{\"token\":\"$name\",\"channelID\":\"$chid\",\"aps\":\"$name\"}"
echo
