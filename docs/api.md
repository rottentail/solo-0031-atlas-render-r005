# HTTP 协议

所有业务路径以 `/v1` 开头。请求体使用 JSON，素材上传直接使用 PNG 字节。

| 方法和路径 | 请求 | 结果 |
|---|---|---|
| PUT /scenes/{id} | name, expectedRevision, document | 完整场景，200 |
| GET /scenes/{id} | 无 | 完整场景，200 |
| POST /scenes/{id}/state | expectedRevision, state | 完整场景，200 |
| POST /scenes/{id}/snapshots | expectedRevision | 快照和素材闭包，201 |
| PUT /assets/{id} | PNG字节 | 素材及base64字节，201 |
| POST /assets/{id}/retire | 无 | state，200 |
| PUT /masks/{id} | 灰度PNG字节 | 蒙版记录，201 |
| GET /masks/{id} | 无 | 蒙版记录，200或404 |
| POST /masks/{id}/retire | 无 | state，200 |
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

文档字段：width、height、background、layers。每个图层有id、kind、width、height、transform、opacity、fill；transform包括x、y、scaleX、scaleY、degrees。图层可以有clip（x/y/width/height），group使用children，polygon使用points，image使用assetId。solid颜料有color；linear还包括endColor、from、to。示例是可直接提交的完整请求。

非组图层可选 `maskId` 引用一张可复用灰度蒙版，并配合 `maskTransform`（字段同transform，省略时为单位变换）把蒙版放置到文档空间。蒙版按 `maskTransform` 把输出像素中心映回蒙版局部坐标，最近邻读取该点灰度作为覆盖率（0完全遮罩、1完全保留），再与图层opacity和alpha相乘；落在蒙版覆盖范围之外的像素按完全遮罩处理。矩形clip链与蒙版同时生效：clip先做硬包含，蒙版再做软遮罩，二者是与关系。一个蒙版可被同文档或跨文档的任意多个图层引用，渲染时只解码一次。组图层不能携带maskId。

蒙版与素材共用输入限制：PNG 1..2MiB、每边1..1024像素，相同ID只能用相同字节重复导入，异内容返回409。蒙版生命周期为 `available → retired`；退役只阻止引用它的新场景保存和新快照冻结，已冻结快照在闭包内保留蒙版字节，退役后渲染像素不变。冻结快照的摘要同时覆盖文档、素材闭包和蒙版闭包；交换包携带蒙版闭包，导入实例独立渲染应得到完全相同的像素。蒙版复用不改变4000万图层像素的工作预算，成本仍按每个图层的输出覆盖区域累计。

幂等键长度1–80，使用英文字母、数字、下划线和连字符，首位不能是后两者。它在服务实例持久状态中全局生效，不随任务完成、取消、结果清理或进程重启失效。改换请求语义需使用新键。

服务接受相同内容的素材重复上传，退役素材不接受重新上传；蒙版规则与素材一致。交换包导出保留快照状态，并在快照内携带蒙版闭包；导入会创建全新的active场景，原快照不作为目标服务中的快照记录落地。导入目标ID必须未使用；共享素材或共享蒙版必须内容相同且仍可用，否则整个提交409且不写入。
