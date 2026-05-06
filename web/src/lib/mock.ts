import type {
  DashboardSnapshot,
  MetricsSnapshot,
  JournalEntry,
  ProjectResponse,
  Task,
  TaskDetail,
  SkillInfo,
  SkillRegistryEntry,
} from './types'

export const mockDashboard: DashboardSnapshot = {
  generated_at: new Date().toISOString(),
  running: [
    {
      id: 1,
      source: 'github',
      title: 'Fix authentication bug in login flow',
      ws: 'workspace:101',
      started_at: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
      output_preview: 'Running tests...\n✓ auth.test.ts (12 tests)',
    },
    {
      id: 2,
      source: 'slack',
      title: 'Add dark mode support to dashboard',
      ws: 'workspace:102',
      started_at: new Date(Date.now() - 12 * 60 * 1000).toISOString(),
      output_preview: 'Installing dependencies...',
    },
  ],
  pending: [
    {
      id: 3,
      source: 'github',
      title: 'Refactor database connection pool',
      priority: 10,
      created_at: new Date(Date.now() - 30 * 60 * 1000).toISOString(),
    },
    {
      id: 4,
      source: 'slack',
      title: 'Update API documentation',
      priority: 5,
      created_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
    },
    {
      id: 5,
      source: 'github',
      title: 'Fix memory leak in worker process',
      priority: 8,
      created_at: new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString(),
    },
  ],
  pending_count: 5,
  recent_24h: {
    done_count: 12,
    failed_count: 2,
    skipped_count: 3,
  },
  sources: [
    {
      name: 'github',
      auth_status: 'authenticated',
      last_listed_at: new Date(Date.now() - 2 * 60 * 1000).toISOString(),
    },
    {
      name: 'slack',
      auth_status: 'authenticated',
      last_listed_at: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
    },
    {
      name: 'linear',
      auth_status: 'expired',
      last_listed_at: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
    },
  ],
}

export const mockMetrics: MetricsSnapshot = {
  total_tasks: 147,
  by_status: {
    done: 98,
    failed: 12,
    skipped: 18,
    pending: 5,
    running: 2,
    waiting_human: 12,
  },
  by_source: {
    github: 85,
    slack: 42,
    linear: 20,
  },
  success_rate: 0.857,
  avg_duration_seconds: 320,
  daily_counts: Array.from({ length: 30 }, (_, i) => {
    const d = new Date('2026-01-01')
    d.setDate(d.getDate() + i)
    const done = (i % 7) + 1
    const failed = i % 3 === 0 ? 1 : 0
    return { date: d.toISOString().slice(0, 10), done, failed }
  }),
}

export const mockJournalEntries: JournalEntry[] = [
  {
    time: new Date(Date.now() - 30 * 60 * 1000).toISOString(),
    source: 'github',
    summary: 'Dispatched task: Fix authentication bug in login flow',
  },
  {
    time: new Date(Date.now() - 45 * 60 * 1000).toISOString(),
    source: 'system',
    summary: 'Task completed: Update README with new examples',
  },
  {
    time: new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString(),
    source: 'slack',
    summary: 'New task discovered: Add dark mode support',
  },
  {
    time: new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString(),
    source: 'github',
    summary: 'Task skipped: Low priority cleanup task',
  },
]

export const mockProject: ProjectResponse = {
  phases: [
    {
      name: 'Phase 1: Foundation',
      status: 'done',
      items: [
        { id: '10', title: 'Set up project structure', status: 'done' },
        { id: '11', title: 'Configure CI/CD pipeline', status: 'done' },
        { id: '12', title: 'Implement core data models', status: 'done' },
      ],
    },
    {
      name: 'Phase 2: Core Features',
      status: 'running',
      items: [
        { id: '13', title: 'Build task discovery engine', status: 'done' },
        { id: '14', title: 'Implement task execution', status: 'running' },
        { id: '15', title: 'Add metrics collection', status: 'pending' },
      ],
    },
    {
      name: 'Phase 3: UI & Polish',
      status: 'pending',
      items: [
        { id: '16', title: 'Build web dashboard', status: 'running' },
        { id: '17', title: 'Add notifications', status: 'pending' },
      ],
    },
  ],
}

export const mockTasks: Task[] = [
  {
    id: 1,
    source: 'github',
    external_id: 'issue-123',
    external_url: 'https://github.com/example/repo/issues/123',
    title: 'Fix authentication bug in login flow',
    body: 'Users report being logged out unexpectedly when using 2FA.',
    notes: 'Reproduces with Google OAuth only',
    status: 'running',
    judgment_reason: 'High priority security issue',
    priority: 10,
    lock_key: 'auth-service',
    cwd: '/home/user/repo',
    ws: 'workspace:101',
    result_summary: '',
    reflection: '',
    created_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
    updated_at: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
    started_at: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
    completed_at: null,
  },
  {
    id: 2,
    source: 'slack',
    external_id: 'msg-456',
    external_url: '',
    title: 'Add dark mode support to dashboard',
    body: 'The dashboard needs a dark mode toggle.',
    notes: '',
    status: 'pending',
    judgment_reason: 'User requested feature',
    priority: 5,
    lock_key: '',
    cwd: '',
    ws: '',
    result_summary: '',
    reflection: '',
    created_at: new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString(),
    updated_at: new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString(),
    started_at: null,
    completed_at: null,
  },
]

export const mockTaskDetail: TaskDetail = {
  ...mockTasks[0],
  audit_entries: [
    {
      time: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      action: 'task.created',
      task_id: 1,
      value: '',
    },
    {
      time: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
      action: 'dispatch.start',
      task_id: 1,
      value: 'workspace:101',
    },
  ],
}

export const mockSkillsInstalled: SkillInfo[] = [
  {
    name: 'review-fix-loop',
    description: 'Code review and auto-fix loop',
    version: '1.2.0',
    installed_at: new Date(Date.now() - 7 * 24 * 60 * 60 * 1000).toISOString(),
  },
  {
    name: 'marunage-triage',
    description: 'Task triage and prioritization',
    version: '2.0.1',
    installed_at: new Date(Date.now() - 3 * 24 * 60 * 60 * 1000).toISOString(),
  },
]

export const mockSkillsRegistry: SkillRegistryEntry[] = [
  {
    name: 'deploy-helper',
    description: 'Automated deployment helper',
    version: '1.0.0',
    author: 'marunage-team',
  },
  {
    name: 'code-review',
    description: 'AI-powered code review',
    version: '2.1.0',
    author: 'community',
  },
  {
    name: 'security-scan',
    description: 'Security vulnerability scanner',
    version: '0.9.0',
    author: 'security-team',
  },
]
