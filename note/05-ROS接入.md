# 05 ROS 接入（rosbridge / 卡片订阅 / 网络）

## 5.1 订阅链路：卡片不直接订阅话题

核心认知：**卡片本身不订阅话题**，它只是**选一个已存在的 Channel**；
真正的 rosbridge 订阅由**后端适配器**在卡片挂载时自动完成。

```
ROS 数据源(填 topics)
   └─ 每个 topic 自动生成一个 Channel        (adapter.ListChannels)
        └─ 卡片选 Channel → 前端 WS 发 subscribe(channelId)
             └─ 后端 adapter.StartChannel → 向 rosbridge 发 subscribe(topic)
                  └─ msg → 归一化 Frame → frameStore → 渲染器
```

相关代码：`server/internal/adapter/ros/adapter.go`（ListChannels / StartChannel / handleMessage）、
`server/internal/adapter/ros/client.go`（rosbridge WS 客户端）、`diff.go`（ROS1/ROS2 差异唯一落点）。

---

## 5.2 用卡片订阅话题（三步）

1. **建数据源**：新建 → 选「ROS 话题」→ 选 ROS1/ROS2 → 填 `ws://<机器人IP>:9090`（ROS2 填 Domain ID）
   → 填话题列表（换行或逗号分隔，如 `/odom`、`/imu/data`）→ 测试连接 → 保存；
2. **自动建通道**：保存后**每个 topic 生成一个 Channel**；发现不全时，通道选择器里点
   「从数据源发现」或「+ 手工新增」；
3. **建卡片**：新建卡片 → 选该 Channel → 渲染器按 msgType 自动推荐 → 卡片挂载即自动订阅并刷新。

---

## 5.3 ⭐ 网络要求（常被误解）

**需要的是「运行后端的主机 → 机器人 rosbridge 的 9090 端口」TCP 可达。**

- 同一局域网是**最省事的充分条件，但不是必要条件**（异地组网 / VPN / Tailscale 同样可行）；
- **浏览器只连后端 `:8080`，不必与机器人同网**；
- 真正必须同处一个 ROS 图的是 **rosbridge 与话题发布者**——**建议二者同机部署**，可彻底消除这条约束。

| 部署方式 | `bridgeUrl` 填什么 | 说明 |
|---|---|---|
| 后端跑在 PC（最常见） | `ws://10.42.0.1:9090` | PC ↔ 机器人必须可达 |
| 后端跑在机器人上 | `ws://127.0.0.1:9090` | 跳数最少，rosbridge 走本地回环最稳 |
| 跨公网 / 4G | `ws://<VPN内网IP>:9090` | **必须先建 VPN**，不建议把 9090 裸暴露 |

### 若 rosbridge 与发布者不同机

- **ROS2**：两者 `ROS_DOMAIN_ID` 必须一致；DDS 默认靠**多播**发现，多播不过路由 →
  跨网段必须额外配 `ROS_DISCOVERY_SERVER` 或 Fast DDS 的 `ROS_STATIC_PEERS`，
  否则 `ros2 topic list` 在 rosbridge 那台机器上根本看不到话题；
- **ROS1**：两者 `ROS_MASTER_URI` 必须指向同一 roscore。

**机器人 WiFi 热点直连场景**：热点本身就是一个局域网，PC 连上即天然满足，
机器人侧地址通常是热点网关（如 `10.42.0.1`）。用 `hostname -I`（机器人上）或 `ip route`（PC 上看默认网关）确认。

### 排查清单（在跑后端的机器上执行）

```bash
ping <机器人IP>
nc -zv <机器人IP> 9090
curl -v --noproxy '*' http://<机器人IP>:9090      # 注意 --noproxy
```

**隐蔽坑**：后端 Go 的 `websocket.Dialer` 用了 `Proxy: http.ProxyFromEnvironment`——
后端主机若设了 `http_proxy`，拨 rosbridge 会走代理失败（表现为"测试连接超时"）。
启动前 `NO_PROXY=<机器人IP>` 或 `unset http_proxy https_proxy`。

---

## 5.4 ROS1 / ROS2 差异（全部收敛在 `diff.go`）

| 差异点 | ROS1 | ROS2 |
|---|---|---|
| msgType 字符串 | `nav_msgs/Odometry` | `nav_msgs/msg/Odometry`（后端自动归一化） |
| subscribe 的 `type` 字段 | **必填**，缺失会 `status: error` | 可选；填错反而会**静默失败** |
| rosapi 服务名 | `/rosapi/topics`、`/rosapi/topic_type` | 各发行版不一致，`/rosapi/topics_and_types` 优先 |
| 订阅无数据 | 会返回 `status: error` | **静默无报错** → 后端用 3s 超时标记 `no data within 3s`，10s 后重试一次订阅 |

**⭐ ROS1 的硬约束**：`type` 必填，而前端向导**只收话题名、不收 msgType** →
**ROS1 必须依赖 rosapi 在线**自动补类型，否则订阅失败。ROS2 无此要求。

启动：

```bash
# ROS1
sudo apt install ros-noetic-rosbridge-suite
roslaunch rosbridge_server rosbridge_websocket.launch      # 默认 ws://<host>:9090
# ROS2
ros2 launch rosbridge_server rosbridge_websocket_launch.xml
```

rosbridge 监听地址须为 `0.0.0.0`（不能只绑 127.0.0.1），并放行防火墙：`sudo ufw allow 9090/tcp`。

---

## 5.5 msgType → 渲染器映射（`mapping.go`）

| ROS 消息类型 | 归一化帧 | 推荐渲染器 |
|---|---|---|
| `std_msgs/Float*`、`Int*`、`UInt*`、`Bool` | scalar | 仪表 |
| `sensor_msgs/BatteryState` | scalar（取 `voltage`，V） | 仪表 |
| `sensor_msgs/Imu`、`nav_msgs/Odometry` | time_series_sample | XY 折线图 |
| `sensor_msgs/NavSatFix` | geo_pose | 地图 |
| `sensor_msgs/Image`、`CompressedImage` | image | 图像 |
| `sensor_msgs/LaserScan` | histogram | 直方图 |
| `rosgraph_msgs/Log` | log_line | 日志流 |
| 其它 / 未知 | json | 原始数据 |

无 msgType 时会从 msg 结构猜测（`guessMsgType`：latitude+longitude→NavSatFix、twist+pose→Odometry 等），
**自定义消息不一定命中**。

---

## 5.6 性能相关（默认已配好，别乱改）

- 订阅默认 `queue_length = 1`（**丢弃积压，只保留最新**）；
- `throttle_rate = 1000 / maxHz`，下限 20ms；
- **图像通道强制 `compression: jpeg` 且上限 10Hz**，从源头降低 JSON 序列化压力。
- ROS2 订阅后无数据是静默的，靠 3s 超时兜底——看到「连接已断开，正在重连（Ns）」先确认话题是否真有发布。
