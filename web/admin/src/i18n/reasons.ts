// The server's explanations by code (core/reason/codes.go; the list is
// core/reason/testdata/codes.json, which reasons.test.ts checks these
// against). The placeholders are the code's parameters and parts.

export const reasonsZh = {
  returning: {
    insertReturning: 'INSERT ... RETURNING',
    lastInsertId: '不支持 RETURNING：执行后读取 last insert id',
  },
  explain: {
    postgresJson: 'EXPLAIN (FORMAT JSON)',
    mysqlJson: 'EXPLAIN FORMAT=JSON',
    plannerEstimates: '优化器的行数估计',
    coarseEstimates: '估计较粗：只有开启 explain 失败即拒绝时闸门才据此计分',
    analyze: 'EXPLAIN ANALYZE',
    noAnalyze: '不支持 EXPLAIN ANALYZE 采样',
  },
  timeout: {
    postgresStatementTimeout: 'statement_timeout 略小于 cost.queryTimeout，并在请求到期时发送取消请求',
    postgresPooled: 'statement_timeout 是会话设置，事务型连接池不保留；请求到期时的取消请求只有连接池转发时才能停止语句',
    postgresCancelOnly: '请求到期时发送取消请求（DSN 自行设置了 statement_timeout）',
    sessionVariable: '{variable} 略小于 cost.queryTimeout',
    sessionVariableSelectOnly: '{variable} 略小于 cost.queryTimeout（只限制 SELECT）',
    dsnSetsVariable: 'DSN 自行设置了 {variable}，cost.queryTimeout 不在数据库侧生效',
    mysqlWriteKilled: '请求到期时以 KILL QUERY 停止（max_execution_time 只限制 SELECT）',
    mysqlWritePooled: 'max_execution_time 只限制 SELECT；经事务型连接池时无法停止超时的语句（KILL QUERY 可能误停其他客户端的会话）：写语句须由数据库侧限制（DBA）',
  },
  setting: {
    pooled: '{variable} 是会话设置，事务型连接池不保留',
  },
  unkeyedWrite: {
    gateOnly: '数据库不拒绝；由闸门的 WriteGuard 拒绝无键写入',
    dsnOff: 'DSN 关闭了 sql_safe_updates；闸门的 WriteGuard 仍拒绝无键写入',
    dsnOn: 'DSN 设置了 sql_safe_updates',
    dsnUnclear: 'DSN 把 sql_safe_updates 设为 {value}，无法判定；闸门的 WriteGuard 仍拒绝无键写入',
    safeUpdates: 'sql_safe_updates = 1',
  },
  limit: {
    unjudged: '应用之前无法判定：{error}',
  },
  limitUse: {
    reads: '读取',
    writes: '写入',
    readsAfterWrites: '写后读与事务内读取',
    procedures: '存储过程',
    sqlEscape: 'SQL 逃生通道',
  },
  privilege: {
    readOnlyServer: '数据库处于只读状态',
    missing: '账号缺少 {privilege} 权限',
    filterNeedsSelect: '带过滤条件的 {privilege} 还需要 SELECT 权限',
    readsBack: '写后读与事务内的读取也走这条连接，需要 SELECT 权限',
    uncheckable: '无法检查权限：{error}',
  },
  strength: {
    enforced: '强制',
    bestEffort: '尽力',
    unsupported: '无',
  },
  validation: {
    capabilityDenied: '实体 {entity}：授予了 {action}，但连接 {connection} 无法执行（{cause}）',
    limitNotEnforced: '硬上限未在数据库侧强制：数据源 {datasource}，连接 {connection}：{use}的 {key} 为{strength}（{cause}）',
  },
  plan: {
    actionDenied: '{action}：{cause}',
  },
}

export const reasonsEn: typeof reasonsZh = {
  returning: {
    insertReturning: 'INSERT ... RETURNING',
    lastInsertId: 'no RETURNING: exec + last insert id',
  },
  explain: {
    postgresJson: 'EXPLAIN (FORMAT JSON)',
    mysqlJson: 'EXPLAIN FORMAT=JSON',
    plannerEstimates: 'planner row estimates',
    coarseEstimates: 'estimates are coarse: the gate scores them only with explain fail-closed',
    analyze: 'EXPLAIN ANALYZE',
    noAnalyze: 'no EXPLAIN ANALYZE sampling',
  },
  timeout: {
    postgresStatementTimeout: 'statement_timeout just under cost.queryTimeout, and a cancel request at the request deadline',
    postgresPooled: 'statement_timeout is a session setting a transaction pooler does not keep; a cancel request at the request deadline stops the statement only if the pooler forwards it',
    postgresCancelOnly: 'a cancel request at the request deadline (the DSN sets its own statement_timeout)',
    sessionVariable: '{variable} just under cost.queryTimeout',
    sessionVariableSelectOnly: '{variable} just under cost.queryTimeout (SELECT only)',
    dsnSetsVariable: 'the DSN sets {variable}, so cost.queryTimeout is not applied natively',
    mysqlWriteKilled: 'KILL QUERY at the request deadline (max_execution_time stops SELECT only)',
    mysqlWritePooled: 'max_execution_time stops SELECT only, and behind a transaction pooler a statement past its deadline cannot be stopped (KILL QUERY could reach another client\'s session): bound writes on the database side (DBA)',
  },
  setting: {
    pooled: '{variable} is a session setting a transaction pooler does not keep',
  },
  unkeyedWrite: {
    gateOnly: "no database-side refusal; the gate's WriteGuard refuses unkeyed writes",
    dsnOff: "the DSN turns sql_safe_updates off; the gate's WriteGuard still refuses unkeyed writes",
    dsnOn: 'the DSN sets sql_safe_updates',
    dsnUnclear: "the DSN sets sql_safe_updates to {value}, which cannot be judged; the gate's WriteGuard still refuses unkeyed writes",
    safeUpdates: 'sql_safe_updates = 1',
  },
  limit: {
    unjudged: 'cannot be judged before it is applied: {error}',
  },
  limitUse: {
    reads: 'reads',
    writes: 'writes',
    readsAfterWrites: 'reads after writes and in transactions',
    procedures: 'procedures',
    sqlEscape: 'the SQL escape hatch',
  },
  privilege: {
    readOnlyServer: 'the server is read-only',
    missing: 'the account lacks the {privilege} privilege',
    filterNeedsSelect: 'a filtered {privilege} also needs SELECT',
    readsBack: 'reads after writes run on this connection and need SELECT',
    uncheckable: 'the privilege could not be checked: {error}',
  },
  strength: {
    enforced: 'enforced',
    bestEffort: 'best effort',
    unsupported: 'unsupported',
  },
  validation: {
    capabilityDenied: 'entity {entity}: {action} is granted, but connection {connection} cannot run it ({cause})',
    limitNotEnforced: 'hard limit not enforced on the database side: datasource {datasource}, connection {connection}: {key} for {use} is {strength} ({cause})',
  },
  plan: {
    actionDenied: '{action}: {cause}',
  },
}
