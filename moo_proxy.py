import socket, threading

SOCK = "/vol1/@appcenter/moo/app.sock"

def read_request(conn):
    """读取一个完整 HTTP 请求（head + body）。连接结束返回 (None, b'')。"""
    buf = b""
    while b"\r\n\r\n" not in buf:
        d = conn.recv(65536)
        if not d:
            return None, b""
        buf += d
    head, _, rest = buf.partition(b"\r\n\r\n")
    cl = 0
    for l in head.split(b"\r\n")[1:]:
        if l.lower().startswith(b"content-length:"):
            cl = int(l.split(b":")[1].strip())
    need = cl - len(rest)
    while need > 0:
        d = conn.recv(65536)
        if not d:
            return None, b""
        rest += d
        need = cl - len(rest)
    return head, rest

def stream(src, dst):
    try:
        while True:
            d = src.recv(65536)
            if not d:
                break
            dst.sendall(d)
    except Exception:
        pass

def handle(conn):
    """每个 TCP 连接只处理一个请求（响应结束后关闭），
    保证每个请求的 head 都经过 X-Trim 头注入。"""
    try:
        head, body = read_request(conn)
        if head is None:
            return
        head2 = head + b"\r\nX-Trim-Username: fnos\r\nX-Trim-Userid: 1\r\nX-Trim-Isadmin: true"
        # 去掉客户端自带的 Connection 头干扰
        cleaned = []
        skip = False
        for l in head2.split(b"\r\n"):
            if l.lower().startswith(b"connection:"):
                skip = True
            if not skip:
                cleaned.append(l)
            else:
                skip = False
        head2 = b"\r\n".join(cleaned) + b"\r\nConnection: close"
        print("REQ", head.split(b"\r\n")[0][:150], flush=True)
        s = socket.socket(socket.AF_UNIX)
        s.connect(SOCK)
        s.sendall(head2 + b"\r\n\r\n" + body)
        t = threading.Thread(target=stream, args=(s, conn), daemon=True)
        t.start()
        t.join()  # 等响应流（含 SSE）完整送达
        s.close()
    except Exception:
        pass
    finally:
        try:
            conn.close()
        except Exception:
            pass

srv = socket.socket()
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
# 仅本机回环：此代理会为每个请求注入管理员头，绝不能暴露到局域网。
# 远程测试用 ssh -L 13812:127.0.0.1:13812 隧道。
srv.bind(("127.0.0.1", 13812))
srv.listen(32)
print("proxy on 13812", flush=True)
while True:
    c, _ = srv.accept()
    threading.Thread(target=handle, args=(c,), daemon=True).start()
