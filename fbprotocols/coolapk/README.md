# coolapk

Anonymous Coolapk (酷安) backend. No login/cookie; every request signed with the
V3 weak `X-App-Token` (same scheme as RSSHub):

    device = random lowercase UUID; now = unix seconds
    token  = md5(base64("token://com.coolapk.market/c67ef5943784d09750dcfbb31020f0ab?"
             + md5(str(now)) + "$" + device + "&com.coolapk.market")) + device + hex(now)

Feeds (GET /v6/page/dataList?url=<escaped #/feed/…>):
  - hotlist: statList?statType=day&sortField=detailnum (今日热门), 600s
  - news:    digestList 最新动态 (digest feed), 120s

Entries are published verbatim (id/message/url/likenum/pic/dateline/… as sent)
plus flat top-level proto_type/cycle_count. Dedupe is id-only (72h window) in
~/.config/fedlet/coolapk-state.json. Register with tag `coolapk`.