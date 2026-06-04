

* fednet/p2put 在server端可能是集成的，也可能是独立进程
  server端有点重的，可能需要接入多种协议消息流，转换为fednet消息

* fednet/p2put 在client端可能是集成的，也可能是独立进程
  client端是最轻量的，只有一个与的fednet传输，fednet连接入p2p mesh
  client端的UI元素，可以简化到不分协议，也可以复杂到按照协议解析所有的消息

* fedbridge后端使用，接入多种协议，转换写入fednet。
  目前考虑使用http接口发送
  起到类似matterbridge的作用
  重量级包，应该很大

* fbprotocols 不同fed/nonfed协议后端消息源
  matrix/nostr/misskey/tox/tg/...

* fbtransports 不同fed协议传递层，libp2p/toxtcp/iroh-relay/freenet/hturnal
  客户端也要使用，必须轻量

* qlfed UI端
  qlfeditive

* web UI端
