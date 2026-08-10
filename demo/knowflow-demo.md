# KnowFlow 演示知识库

## 支持的文档

KnowFlow 支持 PDF、DOCX、Markdown 和 TXT 四种文档格式。上传接口只负责验证并保存原始文件，文档解析、清洗、分块、向量化和索引由独立的 Go Worker 异步完成。

## 检索与问答

KnowFlow 可以配置 Dense 向量检索、Sparse 全文检索、RRF 融合和 Reranker 重排。默认混合检索同时召回 Dense Top 20 与 Sparse Top 20，使用参数为 60 的 Reciprocal Rank Fusion 合并结果，最终选取 5 个证据分块。

回答通过 Server-Sent Events 流式返回。每条回答保存到 PostgreSQL，并附带真实的文档 ID、文件名、分块 ID、原文片段、页码或段落位置以及检索分数。服务端会过滤模型生成的无效引用编号。

## 离线评测

项目内置 60 条评测问题，对比 Dense only、Sparse only、Dense + Sparse + RRF、Dense + Sparse + RRF + Reranker 四种策略。报告同时输出 JSON 与 Markdown，并包含 Recall@1、Recall@5、Recall@10、MRR、引用命中率、延迟、Token 和估算成本。

## 本地开发

开发环境不配置外部模型凭证时会使用确定性的 Fake ChatModel、Fake Embedder 与 Fake Reranker，因此完整演示和自动化测试可以离线运行，不会调用真实付费模型。
