# 动态插件开发指南

## 一、插件工作原理

```
┌─────────────────────────────────────────────────────────────┐
│                    signal-server (主程序)                    │
│                                                             │
│  事件触发 ──► Manager.OnAccept() ──► 调用插件 ──► 等待响应   │
│                                                             │
└──────────────────────────┬──────────────────────────────────┘
                           │ stdin (JSON 请求)
                           ▼
              ┌────────────────────────┐
              │      插件进程           │
              │  (Python/Go/任意语言)   │
              │                        │
              │  1. 握手 (stdout)       │
              │  2. 读配置 (stdin)      │
              │  3. 处理请求循环        │
              └────────────────────────┘
                           │ stdout (JSON 响应)
                           ▼
```

## 二、通信协议

### 2.1 握手阶段

插件启动后，**必须立即**输出一行握手 JSON 到 stdout：

```json
{"name":"my-plugin","version":"1.0","hooks":["on_accept","on_open"]}
```

| 字段 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `name` | string | ✅ | 插件名称，用于日志 |
| `version` | string | ✅ | 插件版本 |
| `hooks` | []string | ✅ | 支持的 Hook 列表 |

### 2.2 配置阶段

握手后，主程序发送配置（来自 plugin.json 的 config 字段）：

```json
{"type":"config","data":{"deny":"10.0.0.0/8","timeout":300}}
```

插件读取后可初始化内部状态。

### 2.3 运行阶段

主程序调用 Hook 时发送请求：

```json
{"id":1,"method":"on_accept","params":{"proto":"tcp","addr":"1.2.3.4:12345"}}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | int | 请求 ID，0 表示通知（无需响应），>0 需要响应 |
| `method` | string | Hook 名称 |
| `params` | object | 参数对象 |

插件处理后返回响应：

```json
{"id":1,"result":{"allowed":true}}
```

或错误：

```json
{"id":1,"error":"something went wrong"}
```

## 三、Hook 类型

### 3.1 on_accept（连接过滤器）

**触发时机**：新连接接入时

**请求参数**：
```json
{
  "proto": "tcp",           // 协议: tcp 或 udp
  "addr": "1.2.3.4:12345"   // 客户端地址
}
```

**响应结果**：
```json
{
  "allowed": true,   // 是否允许连接
  "reason": "..."    // 拒绝原因（可选，用于日志）
}
```

### 3.2 on_open（连接建立）

**触发时机**：通道建立成功时

**请求参数**：
```json
{
  "proto": "tcp",
  "remote_addr": "1.2.3.4:12345",
  "channel_id": 12345,
  "local_port": 8080
}
```

**响应结果**：无（通知类型）

### 3.3 on_close（连接关闭）

**触发时机**：通道关闭时

**请求参数**：
```json
{
  "channel_id": 12345
}
```

**响应结果**：无（通知类型）

### 3.4 on_data（数据传输）

**触发时机**：数据传输时

**请求参数**：
```json
{
  "channel_id": 12345,
  "dir": "rx",    // 方向: rx=接收, tx=发送
  "bytes": 1024   // 字节数
}
```

**响应结果**：无（通知类型）

## 四、plugin.json 清单文件

每个插件目录必须包含 `plugin.json`：

```json
{
  "name": "my-plugin",
  "version": "1.0",
  "type": "filter",
  "hooks": ["on_accept"],
  "exec": "python3 plugin.py",
  "enabled": true,
  "config": {
    "key1": "value1",
    "key2": 100
  }
}
```

| 字段 | 类型 | 必需 | 默认值 | 说明 |
|------|------|------|--------|------|
| `name` | string | ✅ | - | 插件名称 |
| `version` | string | ✅ | - | 插件版本 |
| `type` | string | ✅ | - | 插件类型: `filter`/`logger`/`alerting` |
| `hooks` | []string | ✅ | - | 支持的 Hook |
| `exec` | string | ✅ | - | 执行命令 |
| `enabled` | bool | ❌ | `true` | 是否启用，设为 `false` 可禁用插件 |
| `config` | object | ❌ | `{}` | 传递给插件的配置 |

### exec 字段格式

| 示例 | 说明 |
|------|------|
| `python3 plugin.py` | 系统命令 + 脚本 |
| `./my-plugin` | 相对路径（相对于插件目录） |
| `/usr/local/bin/my-plugin` | 绝对路径 |
| `node index.js` | Node.js 插件 |
| `bash script.sh` | Bash 脚本 |

## 五、插件类型

| 类型 | 说明 | 推荐 Hook |
|------|------|-----------|
| `filter` | 连接过滤器 | `on_accept` |
| `logger` | 日志记录 | `on_open`, `on_close`, `on_data` |
| `alerting` | 告警通知 | `on_open`, `on_close`, `on_data` |

## 六、控制指令（action）

插件可以在 `on_open` 和 `on_data` 的响应中返回控制指令，让主程序执行操作。

### action: close（断开连接）

```json
{"id":1,"result":{"action":"close","reason":"idle timeout 300s"}}
```

主程序收到后立即断开该连接。

**适用 Hook**：`on_open`、`on_data`

**示例场景**：
- 连接空闲超时后断开
- 检测到异常流量后断开
- 认证失败后断开

**注意**：`on_accept` 不使用 `action`，而是通过 `allowed: false` 拒绝连接。

## 七、错误处理

- 插件崩溃：主程序检测到后跳过该插件
- 插件超时：主程序等待 `-plugin-timeout` 后跳过
- 插件返回错误：主程序记录日志，使用默认行为

## 八、性能建议

1. **on_accept 必须快速**：这是同步调用，会阻塞连接处理
2. **on_open/on_data 是同步调用**：插件响应后主程序才继续，注意性能影响
3. **on_close 是异步通知**：不需要响应
4. **缓存结果**：如 GeoIP 查询，可缓存 IP→国家映射
5. **批量处理**：高频事件可攒批后一次处理
