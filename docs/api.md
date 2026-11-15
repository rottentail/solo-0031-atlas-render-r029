# HTTP 协议

所有业务路径以 `/v1` 开头。请求体使用 JSON，素材上传直接使用 PNG 字节。

| 方法和路径 | 请求 | 结果 |
|---|---|---|
| PUT /scenes/{id} | name, expectedRevision, document | 完整场景，200 |
| GET /scenes/{id} | 无 | 完整场景，200 |
| POST /scenes/{id}/state | expectedRevision, state | 完整场景，200 |
| GET /scenes/{id}/recovery-plan | 无 | 归档恢复预览（阻塞图层与可用素材），200 |
| POST /scenes/{id}/recovery | expectedRevision，replacements（退役素材ID→可用素材ID） | 替换并恢复后的完整场景，200 |
| POST /scenes/{id}/snapshots | expectedRevision | 快照和素材闭包，201 |
| PUT /assets/{id} | PNG字节 | 素材及base64字节，201 |
| POST /assets/{id}/retire | 无 | state，200 |
| GET /snapshots/{id} | 无 | 快照，200 |
| POST /snapshots/{id}/revoke | 无 | state，200 |
| GET /snapshots/{id}/package | 无 | atlas-scene交换包，200 |
| POST /imports/{id} | name, package | 新场景，201 |
| POST /jobs | snapshotId, scale；请求头Idempotency-Key | 任务，202 |
| GET /jobs/{id} | 无 | 任务，200 |
| POST /jobs/{id}/cancel | 无 | 任务，200 |
| POST /jobs/{id}/retry | 无 | 任务，202 |
| GET /artifacts/{id} | 可选If-None-Match | PNG，200或304 |
| DELETE /artifacts/{id} | 无 | state，200 |
| POST /compare | left, right（结果ID） | 像素比较，200 |
| GET /status | 无 | 场景/素材/快照/任务计数和结果字节数 |
| GET /events | 无 | 最多500个有序领域事件 |

`GET /healthz` 不带 `/v1`，返回ready。它表明进程已打开并恢复状态，不包含单独的磁盘可写探测。任务响应省略固定输入的有效载荷，输入细节通过快照接口读取。

归档场景在恢复前先读取 `GET /scenes/{id}/recovery-plan`。预览为只读：`blockedLayers` 按文档顺序列出每个引用退役素材的图像图层（含图层ID、所在组路径path、素材ID与摘要、原因），`availableAssets` 列出当前可作为替换目标的可用素材，`recoverable` 为真时可直接恢复。预览不会修改场景，也不会读取或改写任何快照。

恢复通过一次 `POST /scenes/{id}/recovery` 确认：`replacements` 把每个阻塞图层引用的退役素材ID映射到一个可用素材ID，且必须覆盖全部阻塞素材；退役素材不能替换自身，两个退役素材不能共用同一替换目标。替换在单个持久事务内重写文档并把场景切回active、递增修订；旧快照及其捕获的素材字节保持不变，之后可对恢复后的新修订发起冻结。场景不处于archived返回409；expectedRevision过期、替换目标在确认前被他人退役，或映射不完整时返回409，并在响应`details`中携带刷新后的恢复预览，使调用方可带着上下文重试而不丢失处理位置。输入ID非法返回400，实体不存在返回404。

文档字段：width、height、background、layers。每个图层有id、kind、width、height、transform、opacity、fill；transform包括x、y、scaleX、scaleY、degrees。图层可以有clip（x/y/width/height），group使用children，polygon使用points，image使用assetId。solid颜料有color；linear还包括endColor、from、to。示例是可直接提交的完整请求。

幂等键长度1–80，使用英文字母、数字、下划线和连字符，首位不能是后两者。它在服务实例持久状态中全局生效，不随任务完成、取消、结果清理或进程重启失效。改换请求语义需使用新键。

服务接受相同内容的素材重复上传，退役素材不接受重新上传。交换包导出保留快照状态；导入会创建全新的active场景，原快照不作为目标服务中的快照记录落地。导入目标ID必须未使用；共享素材必须内容相同且仍可用。
