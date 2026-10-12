## 完整双向数据流图

```
Node A (runSoftunPhyport)                         Node B (loportmap + 127.0.0.1:9229)
═════════════════════════                          ═══════════════════════════════════════

  127.0.0.1:45600 ──── physical TCP ──── 127.0.0.1:9559
       │                                           │
    scLocal                                     phys (net.Conn)
       │                                           │
 ┌─────┴─────┐                              ┌──────┴──────┐
 │ xfer1      │ io.Copy(scRemote,scLocal)   │ goroutine1  │ io.Copy(phys, vcConn)
 │ goroutine  │ ◄══════════════════════════  │ bridgeLo    │ ◄════════════════════
 └─────┬─────┘                              └──────┬──────┘
       │                                           │
    scRemote                                    vcConn (net.Conn)
       │                                           │
    vtcp.Conn ── Write()/Read() ────────── vtcp.Conn
       │                                           │
    flushPackets                             flushPackets
       │                                           │
    c.writer(pkt)                            c.writer(pkt)
       │                                           │
    sendPacket                               sendPacket
       │                                           │
    routeWrite                               routeWrite
       │                                           │
    routeToPeer                              routeToPeer
       │                                           │
    WriteToPeer                              WriteToPeer
       │                                           │
    hub.write(pkt)                           hub.write(pkt)
       │                                           │
    carrier.w.WriteMsg(pkt)                  carrier.w.WriteMsg(pkt)
       │                                           │
 ┌─────┴─────┐                              ┌──────┴──────┐
 │   pump    │ msgio.ReadMsg()              │    pump    │ msgio.ReadMsg()
 │           │ ════════════════════════════> │            │ ═══════════════════════>
 └─────┬─────┘                              └──────┬──────┘
       │ libp2p stream (A→B)                      │ libp2p stream (B→A)
       │                                           │
    sink.Inbound(pkt)                         sink.Inbound(pkt)
       │                                           │
    softTun.Send(pkt)                         softTun.Send(pkt)
       │                                           │
    handleTCP(ip,ihl)                         handleTCP(ip,ihl)
       │                                           │
    vtcp.HandleSegment                        vtcp.HandleSegment
       │                                           │
    softTun.Accept()                          softTun.Accept()
       │                                           │
    TCPConn ←── vc栈 ──→ TCPConn            TCPConn ←── vc栈 ──→ TCPConn
       │                                           │
    io.Copy(scLocal,scRemote)                io.Copy(vcConn, phys)
 ┌────┴────┐                               ┌───────┴──────┐
 │ xfer2   │ io.Copy(scLocal,scRemote)     │ goroutine2   │ io.Copy(vcConn,phys)
 │ goroutine│ ══════════════════════════>  │ bridgeLo     │ ═════════════════════>
 └────┬────┘                               └───────┬──────┘
      │                                            │
   scLocal                                     phys (net.Conn)
      │                                            │
 127.0.0.1:45600                           127.0.0.1:9229
```
