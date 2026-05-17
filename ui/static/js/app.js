// qui-sync frontend — Alpine.js state + REST client.
// Kept intentionally small; real work lives in the API layer.

function quiSyncApp() {
  // Tab values are 'backup', 'subscribe', 'publish', 'settings'. Pre-v0.3
  // values 'sync' / 'export' are still in some users' localStorage; the
  // migration in init() rewrites them to the new names so the UI doesn't
  // open onto a dead tab the first time after upgrade.
  const boot = window.__QUI_SYNC_BOOTSTRAP__ || { tab: 'backup', scale: 'default' };
  return {
    tab: boot.tab,
    scale: boot.scale,
    health: { status: 'idle', version: '' },
    // authStatus tracks /api/auth/status. authenticated=true → topnav
    // shows username + Logout. configured=true && authenticated=false
    // → caller is reaching us via local-network bypass; topnav shows
    // "Local" badge. Both false → first-run state, /setup wizard
    // already redirected so we don't render auth chrome.
    authStatus: { configured: false, authenticated: false, username: '', authentication: 'forms' },
    instances: [],
    expanded: {},          // instanceID -> bool
    rulesByInstance: {},   // instanceID -> [rule, ...]
    loadingRules: {},      // instanceID -> bool
    editingCategory: null, // instanceID currently being renamed
    categoryDraft: '',     // new name while editing
    config: null,
    diff: null,
    // exportNote is the free-form markdown the maintainer can attach
    // to the next Commit export. UI state only — sent with the export
    // request and written directly into today's CHANGELOG.md section.
    // Cleared automatically on successful export.
    exportNote: '',
    // Per-rule selection state on the Publish preview. excludedFromExport
    // holds rule keys ("<instance_id>:<qui_id>") the user has unchecked;
    // exportComments holds optional rationale text per rule. State
    // survives across Previews of the same batch — see syncSelectionToDiff
    // — and resets after a successful Commit. Default is "everything
    // included, no comments", matching pre-v0.4.1 behaviour.
    excludedFromExport: new Set(),
    exportComments: {},
    expandedExportComment: {},
    // After a successful export, stash the submitted note so the user
    // can open "Edit note" to fix a typo without re-running export.
    // Resets to '' when the next export runs.
    lastExportNote: '',
    editingExportNote: false,
    editExportNoteDraft: '',
    savingExportNote: false,
    changelog: null,
    toasts: [],
    nextToastId: 1,
    modal: { open: false, title: '', body: '', confirmLabel: 'Confirm', cancelLabel: 'Cancel', resolve: null },
    // Settings tab — drafts live outside `config` so user can edit without
    // affecting the displayed config until Save is clicked.
    quiDraft: { url: '', apiKey: '' },
    // Backup tab — schedules CRUD + global gitignored toggle.
    schedules: [],
    backupGitignored: true,
    // Publish tab — "Hide archive/ from GitHub" toggle. Hydrated from
    // /api/config; default false so first-time users get the archive
    // published as part of the share-repo (the historical behaviour).
    archiveGitignored: false,
    runningSchedule: {},  // scheduleID -> bool while a Run-now request is in flight
    scheduleModal: {
      open: false,
      editing: null,        // schedule.id when editing, null when adding
      preset: 'custom',     // dropdown value; 'custom' = user-typed cron
      saving: false,
      cronHuman: '',
      cronInvalid: false,
      draft: { name: '', cron: '0 3 * * *', enabled: true, instances: [], retention_days: 90, keep_last_n: 7 },
    },
    historyModal: { open: false, scheduleId: '', scheduleName: '', entries: [] },
    addInstanceOpen: false,
    addInstanceDraft: { qui_instance_id: 0, category: '' },
    availableQuiInstances: [],
    disconnectingQui: false,
    // Sync tab
    subscriptions: [],
    expandedSub: {},
    selectedInstanceBySub: {},
    loadingPlan: {},
    planBySub: {},
    quiRulesCache: {}, // instanceID -> [{qui_rule_id, name}]
    applying: {},
    // Keyed by "<slug>:<repo_path>" so the per-row spinner on the
    // "Deactivate now" button in the Removed-by-maintainer table flips
    // independently per rule. Different rules can deactivate in
    // parallel; the table doesn't lock as a whole.
    deactivatingRule: {},
    planCategoryFilter: {},  // slug -> '' (all) or category name
    autoPullInterval: 'off',
    addSubOpen: false,
    addSubEditing: false, // true when the modal is editing an existing subscription
    // displayName is the free-form text the user types in the Name
    // field; slug is derived from it on every keystroke and is what the
    // backend actually stores (folder name on disk + path identifier
    // in API URLs + state key). On edit, displayName mirrors the
    // existing slug because the slug is locked once a subscription
    // exists (renaming would mean moving the clone directory + state
    // rewrite, which we don't support).
    addSubDraft: { displayName: '', slug: '', url: '', branch: 'main', auth_mode: 'public', token: '', target_category: '', target_instance_id: 0 },
    addSubSSHKey: '', // non-empty after phase-1 generate; empty means phase-1 hasn't run yet
    addSubCategoryHints: [], // datalist options when editing — categories detected in the subscription's clone
    // Probe state — populated when the user clicks "Look up what's in
    // this repo" inside the Add modal. categories: [{name, rule_count}].
    addSubProbing: false,
    addSubProbeCategories: [],
    addSubProbeTotal: 0,
    addSubProbeError: '',
    addSubOriginalURL: '',
    applyModal: { open: false, slug: '', instanceName: '', create: 0, update: 0, skip: 0 },
    hideDevBanner: localStorage.getItem('qui.hideDevBanner') === 'true',
    hideExportHelp: localStorage.getItem('qui.hideExportHelp') === 'true',
    hideSyncHelp: localStorage.getItem('qui.hideSyncHelp') === 'true',
    hideBackupHelp: localStorage.getItem('qui.hideBackupHelp') === 'true',
    expandedBackup: {},
    backupList: {},      // instanceID -> [{id, timestamp, rule_count}]
    backupCounts: {},    // instanceID -> count
    backingUp: {},
    restoringBackup: false,
    repoStatus: null,

    // Rule-level diff of working tree vs origin/<branch> — the structured
    // pre-push review. Kept separate from repoStatus because the server
    // recomputes the full tree-walk, which is heavier than status.
    ruleDiff: null,
    ruleDiffLoading: false,
    ruleDiffError: '',
    // Set of diff row paths currently expanded to show field details or
    // full-rule JSON. Keyed by change-type + path to keep added/removed of
    // the same rule path independent.
    ruleDiffExpanded: {},
    // Per-row viewer mode inside an expanded Modified row:
    //   "" (default) = structured field list
    //   "json"       = raw before/after JSON blocks (escape hatch)
    ruleDiffViewMode: {},

    // Draft state for push-token save with live validation feedback.
    pushTokenValidating: false,
    pushTokenValidated: '',       // login name on success, '' otherwise
    pushTokenValidateError: '',

    // Busy flag for the destructive "Reset local to remote" action.
    resettingRepo: false,
    loadingRepoStatus: false,
    pushingRepo: false,
    pullingRepo: false,
    pushTokenDraft: '',
    gitRemoteDraft: '',
    repoDisplayNameDraft: '',  // optional friendly label, persisted in config.yml
    gitRemoteStatus: '',  // '' | 'ok' | 'none'
    repoModal: { open: false, editing: false },
    testingQui: false,
    quiTestResult: '',  // '' | 'ok' | 'fail'
    quiTestError: '',
    loading: { instances: false, export: false, config: false, changelog: false },

    init() {
      // Migrate pre-v0.3 tab names so a user with an old `qui.tab`
      // localStorage value (sync / export) lands on a real tab instead
      // of a blank section. Idempotent — fresh installs and migrated
      // ones are both no-ops on the second call.
      const tabRename = { sync: 'subscribe', export: 'publish' };
      if (tabRename[this.tab]) {
        this.tab = tabRename[this.tab];
        localStorage.setItem('qui.tab', this.tab);
      }
      this.fetchAuthStatus();
      this.ping();
      this.loadInstances();
      this.loadConfig();
      this.loadChangelog();
      this.loadSubscriptions();
      this.loadRepoStatus();
      this.loadGitRemote();
      this.loadBackups();
      this.loadSchedules();
      setInterval(() => this.ping(), 30000);
    },

    // CSRF helper — reads quisync_csrf cookie set by the server on
    // every GET. Used inside the logout <form>; AJAX fetches get the
    // X-CSRF-Token header attached automatically by the api.js wrapper.
    csrfToken() {
      const m = document.cookie.match(/(?:^|; )quisync_csrf=([^;]+)/);
      return m ? m[1] : '';
    },

    // fetchAuthStatus pulls the public /api/auth/status payload so the
    // topnav can render username + Logout (when session-authenticated)
    // or "Local" (when reaching us via Trusted-Networks bypass).
    // Failure leaves authStatus at its zero-value — no auth chrome.
    async fetchAuthStatus() {
      try {
        const resp = await fetch('/api/auth/status', { headers: { 'X-Skip-Login-Redirect': '1' } });
        if (!resp.ok) return;
        this.authStatus = await resp.json();
      } catch (e) {
        // Pre-setup or transient failure — leave defaults.
      }
    },

    setTab(t) {
      this.tab = t;
      localStorage.setItem('qui.tab', t);
    },
    saveScale() {
      localStorage.setItem('qui.scale', this.scale);
    },

    // Watch for help panel dismissals and persist via localStorage.
    dismissExportHelp() { this.hideExportHelp = true; localStorage.setItem('qui.hideExportHelp', 'true'); },
    dismissSyncHelp()   { this.hideSyncHelp = true;   localStorage.setItem('qui.hideSyncHelp', 'true'); },
    dismissBackupHelp() { this.hideBackupHelp = true;  localStorage.setItem('qui.hideBackupHelp', 'true'); },

    // --- toasts ---

    toast(kind, msg, ttl = 6000) {
      const id = this.nextToastId++;
      this.toasts.push({ id, kind, msg });
      if (ttl > 0) setTimeout(() => this.dismiss(id), ttl);
    },
    dismiss(id) {
      this.toasts = this.toasts.filter((t) => t.id !== id);
    },

    // --- confirm modal (Promise-based) ---

    confirm(opts) {
      return new Promise((resolve) => {
        this.modal = {
          open: true,
          title: opts.title || 'Confirm',
          body: opts.body || '',
          confirmLabel: opts.confirmLabel || 'Confirm',
          cancelLabel: opts.cancelLabel || 'Cancel',
          resolve,
        };
        this.$nextTick(() => {
          const el = this.$refs.modalConfirmBtn;
          if (el) el.focus();
        });
      });
    },
    modalConfirm() {
      const r = this.modal.resolve;
      this.modal = { ...this.modal, open: false, resolve: null };
      if (r) r(true);
    },
    modalCancel() {
      const r = this.modal.resolve;
      this.modal = { ...this.modal, open: false, resolve: null };
      if (r) r(false);
    },

    // --- HTTP helper: unified ok/error handling ---

    async apiFetch(url, opts = {}) {
      let r;
      try {
        r = await fetch(url, opts);
      } catch (e) {
        throw new Error('Network error: ' + e.message);
      }
      const ct = r.headers.get('Content-Type') || '';
      let body;
      if (ct.includes('application/json')) {
        try { body = await r.json(); } catch (_) { body = null; }
      } else {
        body = await r.text();
      }
      if (!r.ok) {
        const msg = (body && body.error) || (typeof body === 'string' && body) || ('HTTP ' + r.status);
        const err = new Error(msg);
        err.status = r.status;
        throw err;
      }
      return body;
    },

    // --- endpoints ---

    async ping() {
      try {
        const j = await this.apiFetch('/api/health');
        this.health = { status: 'ok', version: j.version };
      } catch (_) {
        this.health = { status: 'err', version: '' };
      }
    },

    async loadInstances() {
      this.loading.instances = true;
      try {
        this.instances = await this.apiFetch('/api/instances');
        // Pre-init per-instance reactive keys for backup + export.
        for (const inst of this.instances) {
          const id = inst.qui_instance_id;
          if (!(id in this.expandedBackup)) this.expandedBackup[id] = false;
          if (!(id in this.backingUp)) this.backingUp[id] = false;
          if (!(id in this.backupCounts)) this.backupCounts[id] = 0;
        }
      } catch (e) {
        this.toast('error', 'Failed to load instances: ' + e.message);
      } finally {
        this.loading.instances = false;
      }
    },

    async toggleInstance(id) {
      this.expanded[id] = !this.expanded[id];
      if (this.expanded[id] && !this.rulesByInstance[id]) {
        await this.loadRules(id);
      }
    },

    async loadRules(id) {
      this.loadingRules[id] = true;
      try {
        this.rulesByInstance[id] = await this.apiFetch('/api/instances/' + id + '/rules');
      } catch (e) {
        this.toast('error', 'Failed to load rules for instance ' + id + ': ' + e.message);
      } finally {
        this.loadingRules[id] = false;
      }
    },

    startEditCategory(inst) {
      this.editingCategory = inst.qui_instance_id;
      this.categoryDraft = inst.category;
      this.$nextTick(() => {
        const el = this.$refs.categoryInput;
        if (el) { el.focus(); el.select(); }
      });
    },
    cancelCategoryEdit() {
      this.editingCategory = null;
      this.categoryDraft = '';
    },
    async saveCategory(inst) {
      // @blur fires while we're already navigating away — guard so we
      // don't send a duplicate save.
      if (this.editingCategory !== inst.qui_instance_id) return;
      const newCat = (this.categoryDraft || '').trim();
      const oldCat = inst.category;
      this.editingCategory = null;
      this.categoryDraft = '';
      if (newCat === '' || newCat === oldCat) return;
      if (!/^[a-z0-9][a-z0-9_-]{0,62}$/.test(newCat)) {
        this.toast('error', 'Invalid folder name — lowercase letters, digits, - and _ only');
        return;
      }
      try {
        await this.apiFetch('/api/instances/' + inst.qui_instance_id + '/category', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ category: newCat }),
        });
        inst.category = newCat;
        this.toast('info', 'Folder renamed: ' + oldCat + ' → ' + newCat);
        // Config on disk changed; refresh Settings view next time.
        this.loadConfig();
      } catch (e) {
        this.toast('error', 'Rename failed: ' + e.message);
      }
    },

    async toggleInstanceExclude(inst) {
      const excluded = !inst.excluded;
      try {
        await this.apiFetch('/api/excludes/instance', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ instance_id: inst.qui_instance_id, excluded }),
        });
        inst.excluded = excluded;
        this.toast('info',
          (excluded ? 'Excluded' : 'Included') + ' entire instance: ' + inst.name);
      } catch (e) {
        this.toast('error', 'Toggle failed: ' + e.message);
      }
    },

    async saveSortOrder(instanceId, rule, newValueStr) {
      const n = parseInt(newValueStr, 10);
      if (isNaN(n)) {
        this.toast('error', 'Priority must be a whole number');
        return;
      }
      // If user sets it back to Qui's value, clear the override instead —
      // keeps state.json tidy and lets future Qui changes flow through.
      const clear = n === rule.qui_sort_order;
      try {
        await this.apiFetch('/api/sort-order', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(
            clear
              ? { instance_id: instanceId, rule_id: rule.qui_id, clear: true }
              : { instance_id: instanceId, rule_id: rule.qui_id, sort_order: n }
          ),
        });
        rule.sort_order = clear ? rule.qui_sort_order : n;
        rule.has_sort_override = !clear;
        // Re-sort the list so reordering is immediately visible.
        this.rulesByInstance[instanceId] = [...this.rulesByInstance[instanceId]]
          .sort((a, b) => a.sort_order - b.sort_order || a.name.localeCompare(b.name));
      } catch (e) {
        this.toast('error', 'Save failed: ' + e.message);
      }
    },

    async resetSortOrder(instanceId, rule) {
      try {
        await this.apiFetch('/api/sort-order', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ instance_id: instanceId, rule_id: rule.qui_id, clear: true }),
        });
        rule.sort_order = rule.qui_sort_order;
        rule.has_sort_override = false;
        this.rulesByInstance[instanceId] = [...this.rulesByInstance[instanceId]]
          .sort((a, b) => a.sort_order - b.sort_order || a.name.localeCompare(b.name));
      } catch (e) {
        this.toast('error', 'Reset failed: ' + e.message);
      }
    },

    async toggleExclude(instanceId, rule) {
      const excluded = !rule.excluded;
      try {
        await this.apiFetch('/api/excludes', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ instance_id: instanceId, rule_id: rule.qui_id, excluded }),
        });
        rule.excluded = excluded;
        const inst = this.instances.find((i) => i.qui_instance_id === instanceId);
        if (inst) inst.excluded_count += excluded ? 1 : -1;
        this.toast('info', (excluded ? 'Excluded: ' : 'Included: ') + rule.name);
      } catch (e) {
        this.toast('error', 'Toggle failed: ' + e.message);
      }
    },

    async includeAll(instanceId) {
      await this.bulkExclude(instanceId, false);
    },
    async excludeAll(instanceId) {
      await this.bulkExclude(instanceId, true);
    },
    async bulkExclude(instanceId, excluded) {
      const rules = this.rulesByInstance[instanceId] || [];
      const toChange = rules.filter((r) => r.excluded !== excluded);
      if (toChange.length === 0) return;
      let ok = 0;
      for (const rule of toChange) {
        try {
          await this.apiFetch('/api/excludes', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ instance_id: instanceId, rule_id: rule.qui_id, excluded }),
          });
          rule.excluded = excluded;
          ok++;
        } catch (_) { /* continue; error toast happens once at end */ }
      }
      const inst = this.instances.find((i) => i.qui_instance_id === instanceId);
      if (inst) {
        inst.excluded_count = rules.filter((r) => r.excluded).length;
      }
      this.toast('info', (excluded ? 'Excluded' : 'Included') + ' ' + ok + ' rule' + (ok === 1 ? '' : 's'));
    },

    async loadConfig() {
      this.loading.config = true;
      try {
        this.config = await this.apiFetch('/api/config');
        this.resetQuiDraft();
        this.resetAutoPullInterval();
        if (typeof this.config.backup_gitignored === 'boolean') {
          this.backupGitignored = this.config.backup_gitignored;
        }
        if (typeof this.config.archive_gitignored === 'boolean') {
          this.archiveGitignored = this.config.archive_gitignored;
        }
      } catch (e) {
        this.toast('error', 'Failed to load config: ' + e.message);
      } finally {
        this.loading.config = false;
      }
    },

    resetAutoPullInterval() {
      if (this.config) this.autoPullInterval = this.config.auto_pull_interval || 'off';
    },

    async saveAutoPullInterval() {
      try {
        await this.apiFetch('/api/config/auto-pull-interval', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ interval: this.autoPullInterval }),
        });
        this.toast('info', 'Auto-pull interval: ' + this.autoPullInterval);
      } catch (e) {
        this.toast('error', 'Save failed: ' + e.message);
      }
    },

    async testQuiConnection() {
      this.testingQui = true;
      this.quiTestResult = '';
      try {
        // Test the DRAFT credentials, not the saved ones, so the user
        // can verify a new URL/key before clicking Save. The backend
        // probes Qui ad-hoc with the request-body creds (falling back
        // to whatever is saved when a field is blank) and never
        // persists anything from this call.
        const data = await this.apiFetch('/api/config/qui/test', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            url: this.quiDraft.url || '',
            api_key: this.quiDraft.apiKey || '',
          }),
        });
        this.quiTestResult = 'ok';
        this.toast('info', 'Connected to Qui — ' + data.length + ' instance' + (data.length === 1 ? '' : 's') + ' found.');
      } catch (e) {
        this.quiTestResult = 'fail';
        this.quiTestError = '● ' + e.message;
      } finally {
        this.testingQui = false;
      }
    },

    resetQuiDraft() {
      if (!this.config) return;
      this.quiDraft = { url: this.config.qui.url || '', apiKey: '' };
    },

    // disconnectQui wipes the saved Qui URL + API key after a confirm
    // step. Distinct from "Discard changes" (which only reverts the
    // form fields). Subscriptions and export instances stay configured
    // — they just become unusable until a Qui is reconnected.
    async disconnectQui() {
      const ok = await this.confirm({
        title: 'Disconnect from Qui?',
        body: 'qui-sync will forget the saved Qui URL and API key. ' +
              'Your subscriptions and export instances will stay configured, ' +
              'but Plan / Apply / Auto-sync / Backup all stop working until ' +
              'you connect to a Qui again. You can re-enter the same details ' +
              'later if you change your mind.',
        confirmLabel: 'Disconnect',
      });
      if (!ok) return;
      this.disconnectingQui = true;
      try {
        await this.apiFetch('/api/config/qui', { method: 'DELETE' });
        this.toast('info', 'Disconnected from Qui.');
        // Reload config so the UI clears URL / api_key_present / etc.
        await this.loadConfig();
        // Also clear the local draft so the form mirrors the new state.
        this.quiDraft = { url: '', apiKey: '' };
        this.quiTestResult = '';
        this.quiTestError = '';
        // Drop the cached Qui-side instance list so Subscribe dropdowns
        // empty out — without this they'd still show stale instances
        // until the next Save or page reload.
        this.availableQuiInstances = [];
      } catch (e) {
        this.toast('error', 'Disconnect failed: ' + e.message);
      } finally {
        this.disconnectingQui = false;
      }
    },
    // ---------- Backup schedules CRUD ----------
    //
    // Each schedule is an independent named cron rule. UI cards on the
    // Backup tab call into these handlers; saveSchedule POSTs new ones
    // or PUTs existing ones, deleteSchedule preserves on-disk backup
    // folders (only the schedule definition disappears).

    async loadSchedules() {
      try {
        const r = await fetch('/api/backup/schedules', { headers: { 'X-Skip-Login-Redirect': '1' } });
        if (!r.ok) return;
        const body = await r.json();
        this.schedules = body.schedules || [];
        this.backupGitignored = !!body.gitignored;
      } catch (e) {
        // Pre-setup or transient — leave schedules empty.
      }
    },

    openAddScheduleModal() {
      const all = this.instances.map(i => i.qui_instance_id);
      this.scheduleModal = {
        open: true,
        editing: null,
        preset: 'custom',
        saving: false,
        cronHuman: '',
        cronInvalid: false,
        // Empty instances on the draft = "all configured", same fall-
        // back the backend uses. Pre-checking every instance would
        // freeze that set in stone — if the user later adds a fourth
        // instance the schedule wouldn't pick it up.
        draft: { name: '', cron: '0 3 * * *', enabled: true, instances: [], retention_days: 90, keep_last_n: 7 },
      };
      this._refreshScheduleCronHelp();
    },

    openEditScheduleModal(sched) {
      this.scheduleModal = {
        open: true,
        editing: sched.id,
        preset: this._guessPresetForCron(sched.cron),
        saving: false,
        cronHuman: '',
        cronInvalid: false,
        draft: {
          name: sched.name,
          cron: sched.cron,
          enabled: sched.enabled,
          instances: Array.isArray(sched.instances) ? [...sched.instances] : [],
          retention_days: sched.retention_days || 0,
          keep_last_n: sched.keep_last_n || 0,
        },
      };
      this._refreshScheduleCronHelp();
    },

    // applySchedulePreset picks a cron expression from the dropdown
    // into the editable cron field. Choosing 'custom' leaves whatever
    // the user typed; everything else overwrites — matches what
    // people expect from "preset: pick one then tweak".
    applySchedulePreset() {
      if (this.scheduleModal.preset !== 'custom') {
        this.scheduleModal.draft.cron = this.scheduleModal.preset;
      }
      this._refreshScheduleCronHelp();
    },

    // Watcher-equivalent — Alpine doesn't have $watch on plain props,
    // so call this whenever cron or enabled changes. Keeps the helper
    // line under the cron field in sync without server roundtrips.
    _refreshScheduleCronHelp() {
      const expr = (this.scheduleModal.draft.cron || '').trim();
      const enabled = this.scheduleModal.draft.enabled;
      if (!expr) {
        this.scheduleModal.cronHuman = enabled ? 'Cron is required when enabled.' : 'No cron set — schedule paused.';
        this.scheduleModal.cronInvalid = enabled;
        return;
      }
      if (typeof cronstrue === 'undefined') {
        this.scheduleModal.cronHuman = '';
        this.scheduleModal.cronInvalid = false;
        return;
      }
      try {
        const desc = cronstrue.toString(expr, { use24HourTimeFormat: true });
        this.scheduleModal.cronHuman = enabled ? desc : desc + ' · paused';
        this.scheduleModal.cronInvalid = false;
      } catch (e) {
        this.scheduleModal.cronHuman = 'Invalid cron expression';
        this.scheduleModal.cronInvalid = true;
      }
    },

    _guessPresetForCron(cron) {
      // The dropdown <option value> list mirrored here keeps the
      // "preset → cron string" round-trip clean. If the saved cron
      // matches one of the presets, the dropdown reflects that;
      // otherwise show 'custom' so the user can edit freely.
      const presets = ['@daily', '0 3 * * *', '0 22 * * *', '0 22 * * 1,5', '0 3 * * 0', '0 */6 * * *', '0 */12 * * *'];
      return presets.includes(cron) ? cron : 'custom';
    },

    toggleScheduleInstance(instID) {
      const arr = this.scheduleModal.draft.instances;
      const i = arr.indexOf(instID);
      if (i >= 0) arr.splice(i, 1);
      else arr.push(instID);
    },

    async saveSchedule() {
      this.scheduleModal.saving = true;
      // Re-validate cron now in case the user typed in custom mode.
      this._refreshScheduleCronHelp();
      if (this.scheduleModal.draft.enabled && this.scheduleModal.cronInvalid) {
        this.scheduleModal.saving = false;
        this.toast('error', 'Cron expression is invalid');
        return;
      }
      const isEdit = !!this.scheduleModal.editing;
      const url = isEdit
        ? `/api/backup/schedules/${encodeURIComponent(this.scheduleModal.editing)}`
        : '/api/backup/schedules';
      try {
        await this.apiFetch(url, {
          method: isEdit ? 'PUT' : 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(this.scheduleModal.draft),
        });
        this.scheduleModal.open = false;
        await this.loadSchedules();
        this.toast('info', isEdit ? 'Schedule updated.' : 'Schedule added.');
      } catch (e) {
        this.toast('error', 'Save failed: ' + e.message);
      } finally {
        this.scheduleModal.saving = false;
      }
    },

    async deleteSchedule(sched) {
      const ok = await this.confirm({
        title: 'Delete schedule?',
        body: `This removes "${sched.name}" from the schedule list. Existing backup files on disk are kept — only the schedule itself disappears.`,
        confirmLabel: 'Delete',
        cancelLabel: 'Cancel',
      });
      if (!ok) return;
      try {
        await this.apiFetch(`/api/backup/schedules/${encodeURIComponent(sched.id)}`, { method: 'DELETE' });
        await this.loadSchedules();
        this.toast('info', 'Schedule deleted.');
      } catch (e) {
        this.toast('error', 'Delete failed: ' + e.message);
      }
    },

    // isScheduleRunning is the bulletproof "is this schedule currently
    // running?" check used by the Run-now button's :disabled binding.
    // The plain `runningSchedule[sched.id]` form had a tester reporting
    // a permanently-disabled (forbidden cursor) Run button on a fresh
    // schedule — root cause unclear, but `=== true` removes any chance
    // of an undefined / object-prototype name collision flipping the
    // expression truthy.
    isScheduleRunning(sched) {
      return this.runningSchedule[sched.id] === true;
    },

    async runSchedule(sched) {
      this.runningSchedule[sched.id] = true;
      try {
        // apiFetch already returns the parsed JSON body — calling
        // .json() on it would throw TypeError. v0.3 carried that bug
        // for one release; fix is just to consume body directly.
        const body = await this.apiFetch(`/api/backup/schedules/${encodeURIComponent(sched.id)}/run`, { method: 'POST' });
        if (body && body.failed > 0) {
          this.toast('error', `Run done — ${body.ok} ok, ${body.failed} failed`);
        } else {
          const ok = (body && body.ok) || 0;
          this.toast('info', `Run done — ${ok} instance${ok === 1 ? '' : 's'} backed up`);
        }
        await this.loadBackups();
        await this.loadSchedules();
      } catch (e) {
        this.toast('error', 'Run failed: ' + e.message);
      } finally {
        this.runningSchedule[sched.id] = false;
      }
    },

    // toggleSchedulePause flips a schedule between enabled/paused
    // without opening the edit modal. Reuses the full-update endpoint
    // so server-side validation runs as it would for any edit.
    async toggleSchedulePause(sched) {
      const next = !sched.enabled;
      try {
        await this.apiFetch(`/api/backup/schedules/${encodeURIComponent(sched.id)}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            name: sched.name,
            cron: sched.cron,
            enabled: next,
            instances: sched.instances || [],
            retention_days: sched.retention_days || 0,
            keep_last_n: sched.keep_last_n || 0,
          }),
        });
        await this.loadSchedules();
        this.toast('info', next ? 'Schedule resumed.' : 'Schedule paused.');
      } catch (e) {
        this.toast('error', 'Toggle failed: ' + e.message);
      }
    },

    // openScheduleHistory loads backup folders for every instance the
    // schedule covers, then shows them in a single sortable list. The
    // backups belong to the instance, not the schedule — multiple
    // schedules covering the same instance share folders. The modal
    // header makes that clear so users don't expect strict ownership.
    async openScheduleHistory(sched) {
      const targets = (sched.resolved_instances && sched.resolved_instances.length)
        ? sched.resolved_instances
        : this.instances.map(i => i.qui_instance_id);
      const instByID = {};
      for (const inst of this.instances) {
        instByID[inst.qui_instance_id] = inst.qui_name + ' (#' + inst.qui_instance_id + ')';
      }
      // /api/backups returns map[instanceID][]backups; flatten + sort.
      let entries = [];
      try {
        const r = await fetch('/api/backups', { headers: { 'X-Skip-Login-Redirect': '1' } });
        const body = await r.json();
        for (const id of targets) {
          const list = body[String(id)] || body[id] || [];
          for (const bk of list) {
            entries.push({
              ...bk,
              _instance_name: instByID[id] || ('#' + id),
              _restoreTarget: id,
            });
          }
        }
        entries.sort((a, b) => (a.timestamp < b.timestamp ? 1 : -1));
      } catch (e) {
        // fall through with empty list
      }
      this.historyModal = {
        open: true,
        scheduleId: sched.id,
        scheduleName: sched.name,
        entries,
      };
    },

    async saveBackupGitignored() {
      try {
        await this.apiFetch('/api/backup/gitignored', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ gitignored: this.backupGitignored }),
        });
        this.toast('info', this.backupGitignored ? 'Backups will be hidden from GitHub.' : 'Backups will be visible to GitHub.');
      } catch (e) {
        this.toast('error', 'Save failed: ' + e.message);
      }
    },

    async saveArchiveGitignored() {
      try {
        await this.apiFetch('/api/repo/archive-gitignored', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ gitignored: this.archiveGitignored }),
        });
        this.toast(
          'info',
          this.archiveGitignored
            ? 'archive/ will be hidden from GitHub on the next Commit export / Push.'
            : 'archive/ will be published to GitHub on the next Commit export / Push.'
        );
        // Refresh the rule-diff so the user sees .gitignore staged in
        // the pre-push review.
        if (typeof this.loadRuleDiff === 'function') this.loadRuleDiff();
      } catch (e) {
        this.toast('error', 'Save failed: ' + e.message);
      }
    },

    // ---------- Schedule card display helpers ----------

    describeScheduleInstances(sched) {
      const list = sched.resolved_instances || [];
      if (!list.length) return 'none';
      // For 1-2 instances show names; beyond that show count + name
      // count so the row doesn't grow too wide and stays aligned with
      // the rest of the table.
      if (!sched.instances || sched.instances.length === 0) {
        return list.length === 1 ? this._instanceName(list[0]) : `all (${list.length})`;
      }
      if (list.length <= 2) {
        return list.map(id => this._instanceName(id)).join(', ');
      }
      return `${list.length} selected`;
    },

    // describeScheduleInstanceList is the tooltip text — full list of
    // instance names so hover gives the user the detail the compact
    // cell hides.
    describeScheduleInstanceList(sched) {
      const list = sched.resolved_instances || [];
      if (!list.length) return 'no instances';
      return list.map(id => this._instanceName(id)).join(', ');
    },

    _instanceName(instID) {
      const inst = this.instances.find(i => i.qui_instance_id === instID);
      return inst ? inst.qui_name : ('#' + instID);
    },

    describeScheduleCron(sched) {
      const expr = (sched.cron || '').trim();
      if (!expr) return 'no schedule';
      if (typeof cronstrue === 'undefined') return expr;
      try {
        const desc = cronstrue.toString(expr, { use24HourTimeFormat: true });
        return sched.enabled ? desc : desc + ' (paused)';
      } catch {
        return expr + ' (invalid)';
      }
    },

    // relativeTime turns an ISO timestamp into "in 2h", "in 4 days",
    // "1 day ago" etc. for the next/last-run line. Missing inputs
    // collapse to empty so the template's x-show binding hides the
    // span. Uses Intl.RelativeTimeFormat for locale-correct output.
    relativeTime(iso) {
      if (!iso) return '';
      const target = new Date(iso);
      if (isNaN(target.getTime())) return '';
      const diffMs = target.getTime() - Date.now();
      const fmt = new Intl.RelativeTimeFormat(navigator.language || 'en', { numeric: 'auto' });
      const abs = Math.abs(diffMs);
      let value, unit;
      if (abs < 60_000)        { value = Math.round(diffMs / 1000);              unit = 'second'; }
      else if (abs < 3_600_000){ value = Math.round(diffMs / 60_000);            unit = 'minute'; }
      else if (abs < 86_400_000){ value = Math.round(diffMs / 3_600_000);        unit = 'hour'; }
      else if (abs < 2_592_000_000){ value = Math.round(diffMs / 86_400_000);    unit = 'day'; }
      else if (abs < 31_536_000_000){ value = Math.round(diffMs / 2_592_000_000); unit = 'month'; }
      else { value = Math.round(diffMs / 31_536_000_000); unit = 'year'; }
      return fmt.format(value, unit);
    },

    async saveQuiConfig() {
      this.loading.config = true;
      try {
        const body = { url: this.quiDraft.url };
        if (this.quiDraft.apiKey) body.api_key = this.quiDraft.apiKey;
        await this.apiFetch('/api/config/qui', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        await this.loadConfig();
        await this.loadInstances();
        // Refresh the Qui-side instance list so the Subscribe-tab
        // pickers (Add subscription modal + per-row "Sync to"
        // dropdown) get populated as soon as Qui is reachable. Without
        // this, a pure subscriber who only just configured Qui sees
        // empty dropdowns until they reload the page.
        await this.loadAvailableQuiInstances();
        this.toast('info', 'Qui connection saved.');
      } catch (e) {
        this.toast('error', 'Save failed: ' + e.message);
      } finally {
        this.loading.config = false;
      }
    },


    async openAddInstanceModal() {
      this.addInstanceDraft = { qui_instance_id: 0, category: '' };
      try {
        this.availableQuiInstances = await this.apiFetch('/api/qui-instances');
      } catch (e) {
        this.toast('error', 'Could not reach Qui: ' + e.message);
        return;
      }
      this.addInstanceOpen = true;
    },

    async addInstance() {
      const d = this.addInstanceDraft;
      if (!d.qui_instance_id || !d.category) return;
      try {
        await this.apiFetch('/api/instances', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(d),
        });
        this.addInstanceOpen = false;
        await this.loadConfig();
        await this.loadInstances();
        this.toast('info', 'Instance added.');
      } catch (e) {
        this.toast('error', 'Add failed: ' + e.message);
      }
    },

    // ============ Backup tab methods ============

    toggleBackupInstance(id) {
      this.expandedBackup[id] = !this.expandedBackup[id];
      if (this.expandedBackup[id] && !this.backupList[id]) this.loadBackups();
    },

    async loadBackups() {
      try {
        const data = await this.apiFetch('/api/backups');
        for (const inst of this.instances) {
          const id = inst.qui_instance_id;
          this.backupList[id] = data[id] || [];
          this.backupCounts[id] = this.backupList[id].length;
          // Pre-set restore target to same instance.
          for (const bk of this.backupList[id]) {
            if (!bk._restoreTarget) bk._restoreTarget = id;
          }
        }
      } catch (e) {
        this.toast('error', 'Load backups: ' + e.message);
      }
    },

    async createBackup(inst) {
      this.backingUp[inst.qui_instance_id] = true;
      try {
        const r = await this.apiFetch('/api/backup/' + inst.qui_instance_id, { method: 'POST' });
        this.toast('info', 'Backup created: ' + r.rule_count + ' rules from ' + inst.qui_name);
        await this.loadBackups();
      } catch (e) {
        this.toast('error', 'Backup failed: ' + e.message);
      } finally {
        this.backingUp[inst.qui_instance_id] = false;
      }
    },

    async restoreBackup(sourceInstanceID, bk) {
      const target = this.instances.find((i) => i.qui_instance_id === bk._restoreTarget);
      const targetLabel = target ? target.qui_name : 'instance #' + bk._restoreTarget;
      const isCrossInstance = bk._restoreTarget !== sourceInstanceID;
      const sourceLabel = (() => {
        const src = this.instances.find((i) => i.qui_instance_id === sourceInstanceID);
        return src ? src.qui_name : 'instance #' + sourceInstanceID;
      })();
      const crossWarning = isCrossInstance
        ? '⚠ Cross-instance restore — these rules were taken from ' + sourceLabel +
          ' but will land in ' + targetLabel + '. This is a migration, not a same-instance recovery.\n\n'
        : '';
      const ok = await this.confirm({
        title: isCrossInstance ? 'Migrate backup to a different instance?' : 'Restore backup?',
        body: crossWarning +
              'Restore ' + bk.rule_count + ' rules from backup ' + bk.timestamp +
              ' to ' + targetLabel +
              '. Existing rules with matching names will be overwritten. New rules will be created.',
        confirmLabel: isCrossInstance ? 'Migrate' : 'Restore',
      });
      if (!ok) return;
      this.restoringBackup = true;
      try {
        const r = await this.apiFetch('/api/backup/restore', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            instance_id: sourceInstanceID,
            timestamp: bk.timestamp,
            target_instance_id: bk._restoreTarget,
          }),
        });
        this.toast('info', 'Restored: ' + r.created + ' created, ' + r.updated + ' updated' +
                  (r.failed ? ', ' + r.failed + ' failed' : ''));
      } catch (e) {
        this.toast('error', 'Restore failed: ' + e.message);
      } finally {
        this.restoringBackup = false;
      }
    },

    // ============ Sync tab methods ============

    // loadAvailableQuiInstances populates the list of Qui instances
    // available on the connected Qui server. Used by both Publish
    // (Add export instance modal) AND Subscribe (target-instance picker
    // in the Add/Edit subscription modal + the per-subscription "Sync to"
    // dropdown). Loading here on Subscribe init means a pure subscriber
    // — someone who never publishes anything — still gets a populated
    // dropdown without first having to add an export-instance on Publish.
    //
    // Best-effort: a missing Qui connection just leaves the list empty,
    // and the UI falls back to a "pick later when applying" placeholder.
    async loadAvailableQuiInstances() {
      try {
        this.availableQuiInstances = await this.apiFetch('/api/qui-instances');
      } catch (e) {
        // Qui not configured yet, or unreachable — silent fallback so
        // the rest of the Subscribe tab still renders.
        this.availableQuiInstances = [];
      }
    },

    async loadSubscriptions() {
      try {
        this.subscriptions = await this.apiFetch('/api/subscriptions');
        // Pre-initialize per-slug reactive keys so Alpine detects changes
        // when the user interacts with dropdowns and buttons. Without this,
        // adding a new key to a proxy object doesn't trigger re-evaluation
        // of :disabled and other bindings that read the key.
        for (const sub of this.subscriptions) {
          if (!(sub.slug in this.selectedInstanceBySub)) this.selectedInstanceBySub[sub.slug] = 0;
          if (!(sub.slug in this.expandedSub)) this.expandedSub[sub.slug] = false;
          if (!(sub.slug in this.loadingPlan)) this.loadingPlan[sub.slug] = false;
          if (!(sub.slug in this.applying)) this.applying[sub.slug] = false;
          if (!(sub.slug in this.planCategoryFilter)) this.planCategoryFilter[sub.slug] = '';
        }
        // Pure subscribers (no export-instances configured) need Qui
        // instances loaded so their "Apply to which Qui instance" + "Sync
        // to" dropdowns aren't empty. Best-effort — silent fallback if
        // Qui isn't configured yet.
        this.loadAvailableQuiInstances();
      } catch (e) {
        this.toast('error', 'Load subscriptions: ' + e.message);
      }
    },

    toggleSub(slug) {
      this.expandedSub[slug] = !this.expandedSub[slug];
      // Pre-fill the "Sync to" dropdown from the subscription's saved
      // target_instance_id so the user doesn't pick the same instance
      // every time they expand a row. selectedInstanceBySub is the
      // local UI state; falling back to 0 keeps the placeholder option
      // visible until the user makes a choice.
      if (this.expandedSub[slug] && !this.selectedInstanceBySub[slug]) {
        const sub = (this.subscriptions || []).find(s => s.slug === slug);
        if (sub && sub.target_instance_id) {
          this.selectedInstanceBySub[slug] = sub.target_instance_id;
        }
      }
    },

    openAddSubModal() {
      this.addSubDraft = { displayName: '', slug: '', url: '', branch: 'main', auth_mode: 'public', token: '', target_category: '', target_instance_id: 0 };
      this.addSubSSHKey = '';
      this.addSubEditing = false;
      this.addSubCategoryHints = [];
      // Reset the URL-change snapshot so a previous edit-mode open
      // doesn't leak a "URL changed since save" warning into the next
      // add-mode session.
      this.addSubOriginalURL = '';
      this._resetProbeState();
      this.addSubOpen = true;
    },

    // openEditSub re-uses the Add-modal in edit mode. The slug is locked
    // (it's the clone-dir name on disk and renaming would require moving
    // files); everything else is editable. Token field is left empty —
    // submitting blank tells the backend to keep the existing PAT.
    openEditSub(sub) {
      this.addSubDraft = {
        // In edit mode, displayName equals the existing slug — the slug
        // is locked (it's the on-disk clone dir + the state key) and we
        // mirror it into displayName so the Name field shows something
        // sensible even though the field itself is read-only here.
        displayName: sub.slug,
        slug: sub.slug,
        url: sub.url,
        branch: sub.branch || 'main',
        auth_mode: sub.auth_mode || 'public',
        token: '',
        target_category: sub.target_category || '',
        target_instance_id: sub.target_instance_id || 0,
      };
      // Snapshot of the original URL so the modal can flag a URL
      // change as destructive (the backend re-clones from scratch
      // when the URL changes; this is correct, but it's worth a
      // visible warning before the user clicks Save).
      this.addSubOriginalURL = sub.url || '';
      this.addSubSSHKey = '';
      this.addSubEditing = true;
      this.addSubCategoryHints = Array.isArray(sub.repo_categories) ? sub.repo_categories : [];
      // Pre-populate the probe categories from the existing clone so
      // the user can pick from the same UI without re-probing.
      this.addSubProbeCategories = (sub.repo_categories || []).map(name => ({ name, rule_count: 0 }));
      this.addSubProbeTotal = sub.repo_rule_count || 0;
      this.addSubProbeError = '';
      this.addSubProbing = false;
      this.addSubOpen = true;
    },

    cancelAddSub() {
      this.addSubOpen = false;
      this.addSubSSHKey = '';
      this.addSubEditing = false;
      this._resetProbeState();
    },

    _resetProbeState() {
      this.addSubProbing = false;
      this.addSubProbeCategories = [];
      this.addSubProbeTotal = 0;
      this.addSubProbeError = '';
    },

    // probeSubscription does a lightweight clone via the backend so the
    // Add modal can populate the category radio list with real options
    // (and rule counts per category) instead of asking the user to type
    // a folder name they may or may not remember.
    async probeSubscription() {
      const d = this.addSubDraft;
      if (!d.url) return;
      this.addSubProbing = true;
      this.addSubProbeError = '';
      try {
        const body = await this.apiFetch('/api/subscriptions/probe', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            slug: d.slug,
            url: d.url,
            branch: d.branch || 'main',
            auth_mode: d.auth_mode,
            token: d.token,
          }),
        });
        this.addSubProbeCategories = (body && body.categories) || [];
        this.addSubProbeTotal = (body && body.total) || 0;
        if (this.addSubProbeCategories.length === 0) {
          this.addSubProbeError = 'No categories with rule files found in this repo.';
        }
      } catch (e) {
        this.addSubProbeError = e.message || 'Lookup failed';
        this.addSubProbeCategories = [];
        this.addSubProbeTotal = 0;
      } finally {
        this.addSubProbing = false;
      }
    },

    // Button label reflects the current phase of the flow.
    addSubNextLabel() {
      if (this.addSubEditing) return 'Save';
      if (this.addSubDraft.auth_mode === 'ssh_deploy_key' && !this.addSubSSHKey) {
        return 'Generate deploy key';
      }
      return 'Add';
    },

    // slugifyName derives a filesystem-safe identifier from a free-form
    // name. Mirrors the backend's `^[a-z0-9][a-z0-9_-]{0,62}$` rule.
    // Called as the user types so the live preview below the field
    // shows exactly what will be saved on disk + sent over the wire.
    slugifyName(name) {
      const s = (name || '')
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, '-')
        .replace(/-+/g, '-')
        .replace(/^-+|-+$/g, '');
      return s.slice(0, 63);
    },

    // updateSubDisplayName fires on every keystroke in the Name input
    // (Add mode only — edit mode locks the slug). The user types whatever
    // they want; we keep the raw displayName for the field's value and
    // derive the slug on the side so the backend never sees the messy
    // form. UI shows the derived slug as a preview below the input.
    updateSubDisplayName(value) {
      this.addSubDraft.displayName = value;
      this.addSubDraft.slug = this.slugifyName(value);
    },

    async addSubscription() {
      const d = this.addSubDraft;
      if (!d.slug || !d.url) return;
      if (!/^[a-z0-9][a-z0-9_-]{0,62}$/.test(d.slug)) {
        // Only triggered when the derived slug is somehow empty or
        // malformed (e.g. user typed only punctuation). UI hints at
        // this via the live preview going blank, but a stricter
        // backstop here keeps a broken POST out of the backend.
        this.toast('error', 'Name produces an empty identifier — type at least one letter or digit.');
        return;
      }

      // Edit path — PUT to existing subscription. SSH key generation is
      // not supported here (key already exists or auth mode is changing).
      if (this.addSubEditing) {
        try {
          await this.apiFetch('/api/subscriptions/' + encodeURIComponent(d.slug), {
            method: 'PUT', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(d),
          });
          this.addSubOpen = false;
          this.addSubEditing = false;
          await this.loadSubscriptions();
          this.toast('info', 'Subscription updated: ' + d.slug);
        } catch (e) {
          this.toast('error', 'Update failed: ' + e.message);
        }
        return;
      }

      // Phase 1 — SSH mode, no key generated yet: create the key, show
      // it inline, and wait for the user to paste it into GitHub before
      // they click Add again.
      if (d.auth_mode === 'ssh_deploy_key' && !this.addSubSSHKey) {
        try {
          const r = await this.apiFetch('/api/subscriptions/generate-key', {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ slug: d.slug }),
          });
          this.addSubSSHKey = r.public;
          this.toast('info', 'Deploy key generated — paste into GitHub, then click Add.');
        } catch (e) {
          this.toast('error', 'Key generation failed: ' + e.message);
        }
        return;
      }

      // Phase 2 — actually register the subscription.
      try {
        await this.apiFetch('/api/subscriptions', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(d),
        });
        this.addSubOpen = false;
        this.addSubSSHKey = '';
        await this.loadSubscriptions();
        this.toast('info', 'Subscription added: ' + d.slug);
      } catch (e) {
        this.toast('error', 'Add failed: ' + e.message);
      }
    },

    async copyAddSubKey() {
      try {
        await navigator.clipboard.writeText(this.addSubSSHKey);
        this.toast('info', 'Public key copied.');
      } catch (e) {
        this.toast('error', 'Clipboard unavailable — select manually.');
      }
    },

    async removeSub(sub) {
      const ok = await this.confirm({
        title: 'Remove subscription?',
        body: 'Remove "' + sub.slug + '"? The local clone and any SSH keys will be deleted. Your Qui rules are untouched.',
        confirmLabel: 'Remove',
      });
      if (!ok) return;
      try {
        await this.apiFetch('/api/subscriptions/' + sub.slug, { method: 'DELETE' });
        await this.loadSubscriptions();
        delete this.expandedSub[sub.slug];
        delete this.planBySub[sub.slug];
        this.toast('info', 'Subscription removed.');
      } catch (e) {
        this.toast('error', 'Remove failed: ' + e.message);
      }
    },

    async pullSub(slug) {
      try {
        const r = await this.apiFetch('/api/subscriptions/' + slug + '/pull', { method: 'POST' });
        await this.loadSubscriptions();
        this.toast('info', 'Pulled ' + slug + ' — HEAD ' + (r.sha || '').slice(0,7));
      } catch (e) {
        this.toast('error', 'Pull failed: ' + e.message);
      }
    },

    async loadQuiRulesFor(instanceId) {
      if (this.quiRulesCache[instanceId]) return;
      try {
        this.quiRulesCache[instanceId] = await this.apiFetch('/api/instances/' + instanceId + '/qui-rules');
      } catch (e) {
        this.quiRulesCache[instanceId] = [];
      }
    },

    async planSub(slug) {
      const instanceId = this.selectedInstanceBySub[slug];
      if (!instanceId) return;
      this.loadingPlan[slug] = true;
      try {
        // Force-reload the Qui-rules cache so a re-Plan after Apply
        // sees the rules just created. loadQuiRulesFor early-returns
        // when the cache is populated; without this delete the
        // dropdown for newly-created rules would lack a "Link: ..."
        // option until a manual page refresh.
        delete this.quiRulesCache[instanceId];
        await this.loadQuiRulesFor(instanceId);
        const plan = await this.apiFetch('/api/subscriptions/' + slug + '/plan', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ qui_instance_id: instanceId }),
        });
        this.planBySub[slug] = plan;
      } catch (e) {
        this.toast('error', 'Plan failed: ' + e.message);
      } finally {
        this.loadingPlan[slug] = false;
      }
    },

    selectAllRules(slug, include) {
      const rules = this.filteredPlanRules(slug);
      for (const rule of rules) {
        if (include && rule.action === 'skip') {
          if (rule.link_suggestion && rule.link_suggestion.qui_rule_id) {
            rule.action = 'update_existing';
            rule.qui_rule_id = rule.link_suggestion.qui_rule_id;
          } else {
            rule.action = 'create_new';
          }
        } else if (!include && rule.action !== 'skip') {
          rule.action = 'skip';
        }
      }
    },

    planCategories(slug) {
      const plan = this.planBySub[slug];
      if (!plan) return [];
      const cats = new Set();
      for (const r of plan.rules) if (r.category) cats.add(r.category);
      return [...cats].sort();
    },

    filteredPlanRules(slug) {
      const plan = this.planBySub[slug];
      if (!plan) return [];
      const cat = this.planCategoryFilter[slug];
      // Skip-action (excluded) rules live in their own section now —
      // hide them from the main table so the user's curated list isn't
      // re-cluttered with rules they've already said no to. The
      // "Excluded by you" section renders them with an Include-again
      // button when the user wants to bring one back.
      const visibleAction = (r) => r.action !== 'skip';
      if (!cat) return plan.rules.filter(visibleAction);
      return plan.rules.filter((r) => r.category === cat && visibleAction(r));
    },

    // excludedPlanRules returns the skip-action rules for the per-plan
    // "Excluded by you" section. Respects the category filter so the
    // section stays in sync with what's visible in the main table.
    excludedPlanRules(slug) {
      const plan = this.planBySub[slug];
      if (!plan) return [];
      const cat = this.planCategoryFilter[slug];
      const onlySkipped = (r) => r.action === 'skip';
      if (!cat) return plan.rules.filter(onlySkipped);
      return plan.rules.filter((r) => r.category === cat && onlySkipped(r));
    },

    // toggleAutoSync handles two cases:
    //   - already-applied rule (rule.from_state == true): hit the API
    //     so the change takes effect immediately. Auto-sync uses the
    //     stored flag the next time it pulls.
    //   - new rule (rule.from_state == false): no state row yet, so
    //     just stage the flag locally. The Apply request will carry
    //     auto_sync: true and the backend persists it after creating
    //     the link. Without this path the user couldn't tick Auto on a
    //     fresh rule until they had Applied once and come back.
    async toggleAutoSync(slug, rule) {
      const autoSync = !rule.auto_sync;
      if (!rule.from_state) {
        rule.auto_sync = autoSync;
        return;
      }
      try {
        await this.apiFetch('/api/subscriptions/' + slug + '/rules/auto-sync', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ repo_path: rule.repo_path, auto_sync: autoSync }),
        });
        rule.auto_sync = autoSync;
      } catch (e) {
        this.toast('error', 'Toggle auto-sync: ' + e.message);
      }
    },

    // bulkMatchByName re-runs name matching against the loaded Qui
    // rules cache. For every plan rule currently set to create_new (or
    // skip), look for a Qui rule with the exact same name; if found,
    // switch the action to update_existing and link to it. Useful
    // after the user has manually overridden some rows or added new
    // rules in Qui since the last Plan.
    async bulkMatchByName(slug) {
      const plan = this.planBySub[slug];
      if (!plan) return;
      const instID = plan.qui_instance_id;
      // Force a fresh cache load so the matcher sees rules added in Qui
      // after the last Plan/dropdown render. Without this, "Match by
      // name" silently no-ops when the cache is empty (e.g. a Qui
      // instance that had 0 rules at Plan time but has rules now).
      delete this.quiRulesCache[instID];
      try {
        await this.loadQuiRulesFor(instID);
      } catch (e) {
        this.toast('error', 'Match by name: failed to load Qui rules — ' + e.message);
        return;
      }
      const cache = this.quiRulesCache[instID] || [];
      if (cache.length === 0) {
        this.toast('info', 'Match by name: this Qui instance has no automations to match against yet.');
        return;
      }
      const liveByName = {};
      const usedIDs = new Set();
      for (const qr of cache) {
        const k = (qr.name || '').trim().toLowerCase();
        // Skip entries with no usable ID — would otherwise short-circuit
        // the `if (liveID && ...)` truthy check downstream and silently
        // hide a real match.
        if (k && qr.qui_rule_id) liveByName[k] = qr.qui_rule_id;
      }
      // First pass: keep already-linked update_existing rows so we
      // don't double-claim a Qui rule that's already pointed at.
      for (const r of plan.rules) {
        if (r.action === 'update_existing' && r.qui_rule_id) {
          usedIDs.add(r.qui_rule_id);
        }
      }
      let matched = 0;
      let alreadyLinked = 0;
      const traces = [];
      for (const r of plan.rules) {
        if (r.action === 'update_existing' && r.qui_rule_id) {
          alreadyLinked++;
          continue;
        }
        const key = (r.name || '').trim().toLowerCase();
        const liveID = liveByName[key];
        const inUsed = liveID ? usedIDs.has(liveID) : false;
        traces.push({ name: r.name, key, liveID, inUsed, action: r.action, ruleID: r.qui_rule_id });
        if (liveID && !inUsed) {
          r.action = 'update_existing';
          r.qui_rule_id = liveID;
          // Mark as freshly auto-matched so the under-dropdown hint
          // reads "auto-matched by name" instead of any stale state
          // breadcrumb still attached to the row.
          r.from_state = false;
          r.link_suggestion = { match_type: 'name', qui_rule_id: liveID, confidence: 'medium' };
          usedIDs.add(liveID);
          matched++;
        }
      }
      // Per-rule lookup trace — the side-by-side dump shows names but
      // not the actual liveByName lookup result, which is the only way
      // to spot "name is in both lists but lookup returned undefined"
      // bugs (e.g. cache entry with qui_rule_id=0).
      console.group('qui-sync Match-by-name: per-rule lookup');
      console.log('liveByName keys (' + Object.keys(liveByName).length + '):', Object.keys(liveByName));
      for (const t of traces) console.log('  →', t);
      console.groupEnd();
      if (matched > 0) {
        this.toast('info', 'Matched ' + matched + ' rule' + (matched === 1 ? '' : 's') + ' by name.');
      } else if (alreadyLinked === plan.rules.length) {
        this.toast('info', 'All rules are already linked to a Qui rule.');
      } else {
        // Diagnostic dump — silent toasts hide WHY the match failed.
        // Print both sides side-by-side to the console so the user can
        // see exactly which character is different (trailing space,
        // hyphen vs em-dash, smart-quote, etc.). The toast nudges them
        // to open the console.
        const repoNames = plan.rules
          .filter(r => r.action !== 'update_existing' || !r.qui_rule_id)
          .map(r => r.name || '');
        const quiNames = cache.map(qr => qr.name || '');
        console.group('qui-sync Match-by-name: no matches — diagnostic');
        console.log('Repo rules (unmatched), normalised:');
        for (const n of repoNames) console.log('  ', JSON.stringify(n.trim().toLowerCase()), '   raw:', JSON.stringify(n));
        console.log('Qui rules in instance #' + instID + ', normalised:');
        for (const n of quiNames) console.log('  ', JSON.stringify(n.trim().toLowerCase()), '   raw:', JSON.stringify(n));
        const sharedHints = repoNames.filter(rn => {
          const nrn = rn.trim().toLowerCase();
          return quiNames.some(qn => {
            const nqn = qn.trim().toLowerCase();
            return nrn !== nqn && (nrn.includes(nqn) || nqn.includes(nrn));
          });
        });
        if (sharedHints.length > 0) {
          console.warn('Possible near-matches (one contains the other):', sharedHints);
        }
        console.groupEnd();
        this.toast('info', 'No matches found — names differ between repo and Qui (' +
          plan.rules.length + ' repo, ' + cache.length + ' Qui). Open the browser developer console for a side-by-side comparison if you want to investigate.', 12000);
      }
    },

    // bulkSetAllCreateNew flips every plan rule (regardless of current
    // action) to create_new. Useful when the auto-match guessed wrong
    // links or when the user wants a clean slate before picking each
    // row by hand. Skipped rows are NOT changed — Skip is its own
    // explicit "leave this alone" decision.
    bulkSetAllCreateNew(slug) {
      const plan = this.planBySub[slug];
      if (!plan) return;
      let changed = 0;
      for (const r of plan.rules) {
        if (r.action === 'skip') continue;
        if (r.action !== 'create_new' || r.qui_rule_id) {
          r.action = 'create_new';
          r.qui_rule_id = 0;
          // Bulk-reset is an explicit user action; clear the auto-
          // match / state breadcrumb so the source-hint reflects the
          // new manual state.
          r.from_state = false;
          r.link_suggestion = { match_type: 'manual', confidence: 'high' };
          changed++;
        }
      }
      this.toast('info', changed === 0 ? 'Already all set to Create new.' : 'Reset ' + changed + ' rule' + (changed === 1 ? '' : 's') + ' to Create new.');
    },

    // toggleRuleSkip flips a rule between visible (create_new /
    // update_existing) and excluded (skip). Persists the decision
    // immediately via the rules/decision endpoint so the choice
    // survives a Plan refresh — without that the user would have
    // their exclude list reset every time they pulled, defeating
    // the purpose of having an exclude list at all.
    async toggleRuleSkip(slug, rule) {
      const plan = this.planBySub[slug];
      const instanceID = plan ? plan.qui_instance_id : 0;
      // Compute next action + qui_rule_id locally first so we can
      // optimistically update the UI before the round-trip lands.
      let nextAction, nextQuiID = 0;
      if (rule.action === 'skip') {
        if (rule.link_suggestion && rule.link_suggestion.qui_rule_id) {
          nextAction = 'update_existing';
          nextQuiID = rule.link_suggestion.qui_rule_id;
        } else {
          nextAction = 'create_new';
        }
      } else {
        nextAction = 'skip';
      }
      const prev = { action: rule.action, qui_rule_id: rule.qui_rule_id, from_state: rule.from_state };
      rule.action = nextAction;
      rule.qui_rule_id = nextQuiID;
      rule.from_state = true; // state now reflects this decision
      rule.link_suggestion = { match_type: 'manual', confidence: 'high' };
      try {
        await this.apiFetch('/api/subscriptions/' + slug + '/rules/decision', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            repo_path: rule.repo_path,
            qui_instance_id: instanceID,
            action: nextAction,
            qui_rule_id: nextQuiID,
          }),
        });
      } catch (e) {
        // Roll back the optimistic UI so the row reverts to its
        // previous classification — the user sees their click had no
        // server-side effect rather than a phantom-applied change.
        rule.action = prev.action;
        rule.qui_rule_id = prev.qui_rule_id;
        rule.from_state = prev.from_state;
        this.toast('error', 'Save failed: ' + e.message);
      }
    },

    // includePlanRule is the inverse of toggleRuleSkip for the
    // "Excluded by you" section — flips a skip-action row back to
    // create_new (or update_existing when an auto-match exists) and
    // persists immediately so the row reappears in the main table on
    // this Plan view without a refresh round.
    async includePlanRule(slug, rule) {
      await this.toggleRuleSkip(slug, rule);
    },

    setRulePriority(slug, rule, value) {
      const n = parseInt(value, 10);
      if (isNaN(n)) return;
      rule.sort_order = n;
      rule.sort_order_from = 'override';
    },

    // describeMatchSource is the under-dropdown hint that tells the
    // user where the current value came from. Three sources:
    //   - state    → remembered from a previous Apply (highest trust,
    //                rule.from_state is true)
    //   - name     → server's auto-match by exact name on this Plan run
    //   - slug     → server matched by slug (rare, future-proof)
    //   - none     → no suggestion; will create new
    // The state path is highlighted in blue; everything else stays in
    // the muted text color so the row doesn't look busy.
    describeMatchSource(rule) {
      if (rule.from_state) return '✓ remembered from last apply';
      const m = (rule.link_suggestion || {}).match_type;
      if (m === 'name') return 'auto-matched by name';
      if (m === 'slug') return 'auto-matched by slug';
      if (m === 'manual') return 'manually picked';
      return ''; // none / unmatched — keep row visually quieter
    },

    // setRuleLink handles the per-rule dropdown change (Create new /
    // Skip / Link to <existing-rule>). All three branches persist
    // immediately via the rules/decision endpoint so dropdown picks
    // behave the same way as the row-start checkbox — the user's choice
    // survives a Plan refresh without requiring an Apply round.
    async setRuleLink(slug, rule, value) {
      const plan = this.planBySub[slug];
      const instanceID = plan ? plan.qui_instance_id : 0;
      let nextAction, nextQuiID = 0;
      if (value === 'create_new') {
        nextAction = 'create_new';
      } else if (value === 'skip') {
        nextAction = 'skip';
      } else if (value.startsWith('u:')) {
        nextAction = 'update_existing';
        nextQuiID = parseInt(value.slice(2), 10);
      } else {
        return; // unknown value, ignore
      }
      const prev = { action: rule.action, qui_rule_id: rule.qui_rule_id, from_state: rule.from_state };
      rule.action = nextAction;
      rule.qui_rule_id = nextQuiID;
      rule.from_state = true;
      rule.link_suggestion = { match_type: 'manual', confidence: 'high' };
      try {
        await this.apiFetch('/api/subscriptions/' + slug + '/rules/decision', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            repo_path: rule.repo_path,
            qui_instance_id: instanceID,
            action: nextAction,
            qui_rule_id: nextQuiID,
          }),
        });
      } catch (e) {
        // Roll back so the row reverts visually to the prior state
        // instead of pretending the change took effect.
        rule.action = prev.action;
        rule.qui_rule_id = prev.qui_rule_id;
        rule.from_state = prev.from_state;
        this.toast('error', 'Save failed: ' + e.message);
        return;
      }
    },

    openApplyModal(slug) {
      const plan = this.planBySub[slug];
      if (!plan) return;
      const visible = this.filteredPlanRules(slug);
      const counts = { create_new: 0, update_existing: 0, skip: 0 };
      for (const r of visible) counts[r.action] = (counts[r.action] || 0) + 1;
      const inst = this.instances.find((i) => i.qui_instance_id === plan.qui_instance_id);
      // Apply only sends the visible (filtered) rules. If a category
      // filter is active, surface the implication so the user can't be
      // surprised that rules in other categories aren't being applied
      // this round. The "missing" rules are still in the plan; the
      // user can clear the filter and re-Apply to cover them.
      const totalRules = (plan.rules || []).length;
      const filterActive = !!this.planCategoryFilter[slug];
      const filterCategory = this.planCategoryFilter[slug] || '';
      this.applyModal = {
        open: true,
        slug,
        instanceName: inst ? inst.qui_name : ('Instance ' + plan.qui_instance_id),
        create: counts.create_new,
        update: counts.update_existing,
        skip: counts.skip,
        filterActive,
        filterCategory,
        visibleCount: visible.length,
        totalCount: totalRules,
      };
    },

    async runApply() {
      const slug = this.applyModal.slug;
      const plan = this.planBySub[slug];
      if (!plan) return;
      this.applyModal.open = false;
      this.applying[slug] = true;
      try {
        const visible = this.filteredPlanRules(slug);
        const decisions = visible.map((r) => ({
          repo_path: r.repo_path,
          action: r.action,
          qui_rule_id: r.action === 'update_existing' ? r.qui_rule_id : 0,
          sort_order_override: r.sort_order_from === 'override' ? r.sort_order : null,
          auto_sync: !!r.auto_sync,
        }));
        const result = await this.apiFetch('/api/subscriptions/' + slug + '/apply', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ qui_instance_id: plan.qui_instance_id, decisions }),
        });
        const nC = result.created ? result.created.length : 0;
        const nU = result.updated ? result.updated.length : 0;
        const nF = result.failed ? result.failed.length : 0;
        if (nF > 0) {
          // Surface the actual error reason so the user knows WHY the
          // rule failed — the backend populates Error on every failed
          // outcome but the summary counts alone aren't actionable.
          // Log the full list for inspection; show the first one in
          // the toast so there is something to search for.
          console.warn('qui-sync apply failures:', result.failed);
          const first = result.failed[0] || {};
          const firstDetail = (first.name || first.repo_path || 'rule') + ': ' + (first.error || 'unknown error');
          const more = nF > 1 ? ' (+' + (nF - 1) + ' more — see browser console)' : '';
          // Longer TTL (20s) so the user has time to read the actual
          // error reason before it disappears. Default toast TTL of
          // 6s is not enough for multi-line apply failures.
          this.toast('error',
            'Applied with errors: ' + nC + ' created, ' + nU + ' updated, ' + nF + ' failed — ' + firstDetail + more,
            20000);
        } else {
          this.toast('info', 'Applied: ' + nC + ' created, ' + nU + ' updated');
        }
        // Refresh plan + instances so counts update.
        await this.planSub(slug);
        await this.loadInstances();
      } catch (e) {
        this.toast('error', 'Apply failed: ' + e.message);
      } finally {
        this.applying[slug] = false;
      }
    },

    // Deactivate a single "Removed by maintainer" rule via the apply
    // endpoint (just the deactivate_removed slot, no decisions). Used
    // by the "Deactivate now" button on rules that don't have AutoSync
    // turned on — auto-sync handles the AutoSync ones automatically.
    async deactivateRemoved(slug, rem) {
      const plan = this.planBySub[slug];
      if (!plan) return;
      const key = slug + ':' + rem.repo_path;
      this.deactivatingRule = { ...this.deactivatingRule, [key]: true };
      try {
        const result = await this.apiFetch('/api/subscriptions/' + slug + '/apply', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            qui_instance_id: plan.qui_instance_id,
            decisions: [],
            deactivate_removed: [rem.repo_path],
          }),
        });
        const nD = result.deactivated ? result.deactivated.length : 0;
        const nF = result.failed ? result.failed.length : 0;
        if (nF > 0) {
          const first = result.failed[0] || {};
          this.toast('error',
            'Deactivate failed: ' + ((first.name || first.repo_path) + ' — ' + (first.error || 'unknown')),
            12000);
        } else if (nD > 0) {
          // Distinguish the two outcomes the same endpoint produces:
          // a real flip in Qui (currently_enabled was true) vs a state-
          // only dismiss (rule was already off in Qui — we just marked
          // it so the row drops out of the Removed section). The toast
          // copy needs to match what actually happened or the user
          // thinks something changed in Qui when it didn't.
          const label = rem.last_name || rem.repo_path;
          if (rem.currently_enabled) {
            this.toast('info', 'Turned off: ' + label);
          } else {
            this.toast('info', 'Dismissed: ' + label);
          }
        }
        // Refresh plan so the row updates to AlreadyDeactivated state.
        await this.planSub(slug);
      } catch (e) {
        this.toast('error', 'Deactivate failed: ' + e.message);
      } finally {
        this.deactivatingRule = { ...this.deactivatingRule, [key]: false };
      }
    },

    async removeInstance(inst) {
      const ok = await this.confirm({
        title: 'Remove instance?',
        body: 'Remove Qui instance #' + inst.qui_instance_id +
              ' (folder: ' + inst.category + ') from config? ' +
              'The folder and existing rules stay on disk — re-add to resume, or delete manually.',
        confirmLabel: 'Remove',
      });
      if (!ok) return;
      try {
        await this.apiFetch('/api/instances/' + inst.qui_instance_id, { method: 'DELETE' });
        await this.loadConfig();
        await this.loadInstances();
        this.toast('info', 'Instance removed.');
      } catch (e) {
        this.toast('error', 'Remove failed: ' + e.message);
      }
    },

    async reloadConfig() {
      this.loading.config = true;
      try {
        await this.apiFetch('/api/config/reload', { method: 'POST' });
        await this.loadConfig();
        await this.loadInstances();
        this.toast('info', 'Config reloaded from disk.');
      } catch (e) {
        this.toast('error', 'Reload failed: ' + e.message);
      } finally {
        this.loading.config = false;
      }
    },

    async loadRepoStatus() {
      this.loadingRepoStatus = true;
      try {
        this.repoStatus = await this.apiFetch('/api/repo/status');
      } catch (e) {
        this.toast('error', 'Repo status: ' + e.message);
      } finally {
        this.loadingRepoStatus = false;
      }
      // Rule-level diff always runs after the summary so the user sees
      // both counts and structure from a single Refresh click.
      this.loadRuleDiff();
    },

    async loadRuleDiff() {
      this.ruleDiffLoading = true;
      this.ruleDiffError = '';
      try {
        this.ruleDiff = await this.apiFetch('/api/repo/rule-diff');
      } catch (e) {
        this.ruleDiffError = e.message;
        this.ruleDiff = null;
      } finally {
        this.ruleDiffLoading = false;
      }
    },

    // ruleDiffTotal returns how many rules differ across all three groups.
    // Used by the "Review N changes" header so the user sees the total at
    // a glance before expanding.
    ruleDiffTotal() {
      if (!this.ruleDiff) return 0;
      return (this.ruleDiff.added || []).length
        + (this.ruleDiff.modified || []).length
        + (this.ruleDiff.removed || []).length;
    },

    ruleDiffKey(change, path) {
      return change + ':' + path;
    },

    toggleRuleDiffRow(change, path) {
      const k = this.ruleDiffKey(change, path);
      this.ruleDiffExpanded[k] = !this.ruleDiffExpanded[k];
    },

    setRuleDiffView(change, path, mode) {
      this.ruleDiffViewMode[this.ruleDiffKey(change, path)] = mode;
    },

    ruleDiffViewModeFor(change, path) {
      return this.ruleDiffViewMode[this.ruleDiffKey(change, path)] || '';
    },

    // formatDiffValue renders a single field-diff before/after value as a
    // compact string. Objects and arrays collapse to JSON so the field
    // list stays one line per change. Missing values render as "(none)"
    // so an added-from-nothing or removed-to-nothing change reads as
    // "(none) → <new value>" rather than an opaque blank space.
    formatDiffValue(v) {
      if (v === null || v === undefined) return '(none)';
      if (typeof v === 'string') return v;
      if (typeof v === 'number' || typeof v === 'boolean') return String(v);
      try {
        return JSON.stringify(v);
      } catch (_) {
        return String(v);
      }
    },

    // prettyJSON renders an object or array as 2-space indented JSON for
    // the raw before/after view and the Added/Removed expand body.
    prettyJSON(v) {
      if (v === null || v === undefined) return '';
      if (typeof v === 'string') {
        try {
          return JSON.stringify(JSON.parse(v), null, 2);
        } catch (_) {
          return v;
        }
      }
      try {
        return JSON.stringify(v, null, 2);
      } catch (_) {
        return String(v);
      }
    },

    // --- Push-token validation ---
    async validateStoredPushToken() {
      this.pushTokenValidating = true;
      this.pushTokenValidated = '';
      this.pushTokenValidateError = '';
      try {
        const r = await this.apiFetch('/api/config/push-token/validate', { method: 'POST' });
        this.pushTokenValidated = r.login || 'ok';
      } catch (e) {
        this.pushTokenValidateError = e.message;
      } finally {
        this.pushTokenValidating = false;
      }
    },

    async applyToQui() {
      try {
        await this.apiFetch('/api/repo/apply-to-qui', { method: 'POST' });
        this.toast('info', 'Local repo linked. Switch to the Subscribe tab → expand "__local__" → select instance → Plan → Apply.');
        this.setTab('subscribe');
        await this.loadSubscriptions();
      } catch (e) {
        this.toast('error', 'Apply setup failed: ' + e.message);
      }
    },

    async pullRepo() {
      this.pullingRepo = true;
      try {
        await this.apiFetch('/api/repo/pull', { method: 'POST' });
        await this.loadRepoStatus();
        this.toast('info', 'Pulled latest from remote.');
      } catch (e) {
        this.toast('error', 'Pull failed: ' + e.message);
      } finally {
        this.pullingRepo = false;
      }
    },

    async pushRepo() {
      const ok = await this.confirm({
        title: 'Push to remote?',
        body: 'Push all committed changes in your share-repo to the remote git repository. This makes your exported rules available for others to sync.',
        confirmLabel: 'Push',
      });
      if (!ok) return;
      this.pushingRepo = true;
      try {
        await this.apiFetch('/api/repo/push', { method: 'POST' });
        await this.loadRepoStatus();
        this.toast('info', 'Pushed to remote.');
      } catch (e) {
        this.toast('error', 'Push failed: ' + e.message);
      } finally {
        this.pushingRepo = false;
      }
    },

    // resetLocalToRemote is the escape hatch when local and remote have
    // "unrelated histories" — typically the first-time-setup case where
    // qui-sync's fresh git init can't reconcile with an existing remote
    // repo. Destructive: discards local commits that aren't on the remote.
    async resetLocalToRemote() {
      const ok = await this.confirm({
        title: 'Reset local to remote?',
        body: 'This discards every local commit that is not already on the remote and replaces your /data/repo with an exact copy of the remote branch. Use this on first-time setup when local and remote have diverged histories, or any time you want to start fresh from the remote state. Your Qui itself is not touched.',
        confirmLabel: 'Reset local',
      });
      if (!ok) return;
      this.resettingRepo = true;
      try {
        await this.apiFetch('/api/repo/reset-to-remote', { method: 'POST' });
        await this.loadRepoStatus();
        this.toast('info', 'Local repo reset to remote state. Run Export again to write your Qui rules on top.');
      } catch (e) {
        this.toast('error', 'Reset failed: ' + e.message);
      } finally {
        this.resettingRepo = false;
      }
    },

    async loadGitRemote() {
      try {
        const r = await this.apiFetch('/api/config/git-remote');
        this.gitRemoteStatus = r.configured ? 'ok' : 'none';
        if (r.url) this.gitRemoteDraft = r.url;
        this.repoDisplayNameDraft = r.display_name || '';
      } catch (_) {
        this.gitRemoteStatus = 'none';
      }
    },

    async saveGitConfig() {
      try {
        if (this.gitRemoteDraft) {
          await this.apiFetch('/api/config/git-remote', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              url: this.gitRemoteDraft,
              display_name: this.repoDisplayNameDraft || '',
            }),
          });
        }
        if (this.pushTokenDraft) {
          await this.apiFetch('/api/config/push-token', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ token: this.pushTokenDraft }),
          });
          this.pushTokenDraft = '';
        }
        await this.loadGitRemote();
        await this.loadRepoStatus();
        this.toast('info', 'Git config saved.');
      } catch (e) {
        this.toast('error', 'Save failed: ' + e.message);
      }
    },

    // openRepoModal opens the Set-up / Edit repository modal. In edit
    // mode the URL pre-fills from the saved value and the token field
    // is left blank ("leave blank to keep saved token" placeholder).
    // First-time setup leaves both blank so the user starts fresh.
    openRepoModal() {
      this.repoModal = {
        open: true,
        editing: this.gitRemoteStatus === 'ok',
      };
      // Token field always starts empty in the modal — saving with
      // empty token preserves the existing one (matched by saveGitConfig).
      this.pushTokenDraft = '';
    },

    // saveRepoFromModal wraps saveGitConfig so the modal can show its
    // own validation/save toasts and close on success. Splits the
    // visible save button from the underlying multi-step save (URL +
    // token both go through one click).
    async saveRepoFromModal() {
      if (!this.gitRemoteDraft) return;
      await this.saveGitConfig();
      this.repoModal.open = false;
      // Auto-validate the (newly) saved token so the card immediately
      // shows ✓ or × instead of the neutral "click validate" hint.
      // Skip when in edit mode and the user didn't supply a fresh
      // token — the saved one was already validated previously.
      this.validateStoredPushToken();
    },

    // deleteRepoConfig disconnects the saved repository: server clears
    // the git remote + deletes the token file, then the UI reloads.
    // Local repo files (exported rules, .git, history) stay on disk —
    // re-adding the URL reconnects without losing anything.
    async deleteRepoConfig() {
      const ok = await this.confirm({
        title: 'Disconnect repository?',
        body: 'Removes the GitHub link + saved token. Your local exports and git history stay intact — re-add the URL to reconnect later.',
        confirmLabel: 'Disconnect',
      });
      if (!ok) return;
      try {
        await this.apiFetch('/api/config/git-remote', { method: 'DELETE' });
        this.gitRemoteDraft = '';
        this.repoDisplayNameDraft = '';
        this.pushTokenDraft = '';
        this.pushTokenValidated = '';
        this.pushTokenValidateError = '';
        this.gitRemoteStatus = 'none';
        await this.loadGitRemote();
        this.toast('info', 'Repository disconnected.');
      } catch (e) {
        this.toast('error', 'Disconnect failed: ' + e.message);
      }
    },

    // repoDisplayName derives a friendly label from the URL — last
    // path segment minus the .git suffix. Avoids forcing the user to
    // type a name when most repos already have a clear identity in
    // their URL (github.com/me/qui-automations.git → "qui-automations").
    repoDisplayName() {
      // User-provided friendly label wins over URL-derived. Empty
      // string falls through to the URL derivation so a fresh-from-
      // setup repo still gets a sensible label without forcing the
      // user to type one.
      const explicit = (this.repoDisplayNameDraft || '').trim();
      if (explicit) return explicit;
      const url = (this.gitRemoteDraft || '').trim();
      if (!url) return 'Repository';
      let s = url.replace(/\/+$/, '').replace(/\.git$/, '');
      const i = Math.max(s.lastIndexOf('/'), s.lastIndexOf(':'));
      if (i >= 0) s = s.slice(i + 1);
      return s || url;
    },

    async savePushToken() {
      if (!this.pushTokenDraft) {
        this.toast('error', 'Token is required');
        return;
      }
      try {
        await this.apiFetch('/api/config/push-token', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ token: this.pushTokenDraft }),
        });
        this.pushTokenDraft = '';
        this.toast('info', 'Push token saved.');
        await this.loadRepoStatus();
      } catch (e) {
        this.toast('error', 'Save failed: ' + e.message);
      }
    },

    async loadChangelog() {
      this.loading.changelog = true;
      try {
        this.changelog = await this.apiFetch('/api/changelog');
      } catch (e) {
        this.toast('error', 'Failed to load changelog: ' + e.message);
      } finally {
        this.loading.changelog = false;
      }
    },

    async preview() {
      this.loading.export = true;
      this.diff = null;
      try {
        this.diff = await this.apiFetch('/api/export/preview', { method: 'POST' });
        // Drop selection + comment state for rules that no longer appear
        // in the new preview, but preserve everything that still matches.
        // Lets the user adjust → Preview → adjust again without losing
        // typed comments or checkbox state on rules they care about.
        this.syncSelectionToDiff();
      } catch (e) {
        this.toast('error', 'Preview failed: ' + e.message);
      } finally {
        this.loading.export = false;
      }
    },

    // ruleKey is the stable identifier shared with the backend's
    // ExportOptions.Include / Comments maps. Format MUST match
    // internal/core/export.go::ruleKey to avoid a silent drift.
    ruleKey(e) {
      return (e && (e.instance_id ?? 0)) + ':' + (e && (e.QuiID ?? 0));
    },

    // diffRuleKeys walks every section of the current preview and yields
    // the rule key for each entry. Used to prune selection state and to
    // drive the include array on Commit.
    diffRuleKeys() {
      if (!this.diff) return [];
      const sections = [
        this.diff.added, this.diff.updated, this.diff.renamed,
        this.diff.removed, this.diff.moved,
      ];
      const out = [];
      for (const list of sections) {
        for (const e of (list || [])) {
          out.push(this.ruleKey(e));
        }
      }
      return out;
    },

    isExcludedFromExport(e) {
      return this.excludedFromExport.has(this.ruleKey(e));
    },

    toggleExportEntry(e) {
      const key = this.ruleKey(e);
      if (this.excludedFromExport.has(key)) {
        this.excludedFromExport.delete(key);
      } else {
        this.excludedFromExport.add(key);
      }
      // Force Alpine reactivity — Set mutations don't trigger
      // re-renders on their own.
      this.excludedFromExport = new Set(this.excludedFromExport);
    },

    // toggleExportComment opens / closes the inline rationale editor
    // for one diff row. Comment text persists between collapses so the
    // user can hide a long note for layout without losing it.
    toggleExportComment(e) {
      const key = this.ruleKey(e);
      this.expandedExportComment = {
        ...this.expandedExportComment,
        [key]: !this.expandedExportComment[key],
      };
    },

    exportCommentFor(e) {
      return this.exportComments[this.ruleKey(e)] || '';
    },

    setExportComment(e, value) {
      // Store the raw text — trimming happens server-side on Commit so
      // the user can type and pause mid-sentence without losing
      // intentional leading whitespace.
      const key = this.ruleKey(e);
      this.exportComments = { ...this.exportComments, [key]: value || '' };
    },

    hasExportComment(e) {
      const v = this.exportComments[this.ruleKey(e)];
      return !!(v && v.trim());
    },

    // syncSelectionToDiff drops state for rules that are no longer in
    // the current preview while preserving entries that survived. Called
    // after Preview replaces this.diff so the table renders consistent
    // checkboxes / comments. Skipped: setting up brand-new keys —
    // missing-from-set means "included" already.
    syncSelectionToDiff() {
      const valid = new Set(this.diffRuleKeys());
      const newSet = new Set();
      for (const k of this.excludedFromExport) {
        if (valid.has(k)) newSet.add(k);
      }
      this.excludedFromExport = newSet;
      const newComments = {};
      for (const k of Object.keys(this.exportComments)) {
        if (valid.has(k)) newComments[k] = this.exportComments[k];
      }
      this.exportComments = newComments;
      const newExpanded = {};
      for (const k of Object.keys(this.expandedExportComment)) {
        if (valid.has(k)) newExpanded[k] = this.expandedExportComment[k];
      }
      this.expandedExportComment = newExpanded;
    },

    // resetExportSelection wipes per-rule selection state. Called after
    // a successful Commit since the rules that were processed are gone
    // from the next preview anyway; leaving the Sets populated would
    // just confuse the next batch.
    resetExportSelection() {
      this.excludedFromExport = new Set();
      this.exportComments = {};
      this.expandedExportComment = {};
    },

    // sectionAllExcluded reports whether every row in the given section
    // is currently unchecked. Used to flip the section-level toggle's
    // "Exclude all / Include all" label.
    sectionAllExcluded(entries) {
      if (!entries || entries.length === 0) return false;
      return entries.every(e => this.isExcludedFromExport(e));
    },

    // toggleSectionExclude flips every row in one section: if any are
    // currently included, exclude all; otherwise include all. Each call
    // produces a single new Set instance so Alpine re-renders once.
    toggleSectionExclude(entries) {
      const newSet = new Set(this.excludedFromExport);
      const allExcluded = this.sectionAllExcluded(entries);
      for (const e of (entries || [])) {
        const k = this.ruleKey(e);
        if (allExcluded) newSet.delete(k); else newSet.add(k);
      }
      this.excludedFromExport = newSet;
    },

    async runExport() {
      const ok = await this.confirm({
        title: 'Commit export?',
        body: 'Write pending changes to the share-repo on disk. Files are backed up before overwrite. This does not push to git — you still commit manually.',
        confirmLabel: 'Commit export',
        cancelLabel: 'Cancel',
      });
      if (!ok) return;
      this.loading.export = true;
      try {
        // Any note the user typed goes into the export request body
        // and is written directly into today's CHANGELOG.md section.
        const note = (this.exportNote || '').trim();

        // Build the include filter from the current diff minus
        // anything the user has unchecked. If the preview was empty
        // somehow (shouldn't happen — button only renders for non-empty
        // diffs) fall back to no filter, which preserves the legacy
        // behaviour of "export everything Qui reports".
        const allKeys = this.diffRuleKeys();
        const includeKeys = allKeys.filter(k => !this.excludedFromExport.has(k));
        const skippedCount = allKeys.length - includeKeys.length;

        // Only build a comments map for rules we are actually sending.
        // Skipped rules carry no rationale to record, and stripping them
        // here keeps the wire format minimal.
        const includedSet = new Set(includeKeys);
        const commentsPayload = {};
        for (const k of Object.keys(this.exportComments)) {
          if (!includedSet.has(k)) continue;
          const v = (this.exportComments[k] || '').trim();
          if (v) commentsPayload[k] = v;
        }

        const body = { note };
        if (allKeys.length > 0) {
          // Always send Include when we have a real preview so the
          // backend knows the user reviewed the list — empty array is
          // valid ("nothing this round").
          body.include = includeKeys;
        }
        if (Object.keys(commentsPayload).length > 0) {
          body.comments = commentsPayload;
        }

        this.diff = await this.apiFetch('/api/export/run', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        // Clear the note draft and stash the submitted text so
        // "Edit note" can pre-fill with it if the user needs to fix
        // a typo.
        if (this.diff && !this.diff.empty) {
          this.lastExportNote = note;
          this.exportNote = '';
          this.editingExportNote = false;
          this.resetExportSelection();
        }
        await this.loadChangelog();
        await this.loadRepoStatus();
        if (this.diff.empty) {
          // Could be "nothing changed" OR "user deselected everything".
          // The latter is its own kind of no-op worth flagging so the
          // user understands they didn't accidentally commit something.
          if (includeKeys.length === 0 && allKeys.length > 0) {
            this.toast('info', 'Nothing committed — every change was deselected.');
          } else {
            this.toast('info', 'Nothing to commit — local rules already in sync with Qui.');
          }
        } else {
          const total =
            (this.diff.added   || []).length +
            (this.diff.updated || []).length +
            (this.diff.renamed || []).length +
            (this.diff.removed || []).length +
            (this.diff.moved   || []).length;
          const gitMsg = this.diff.git_committed ? ' + committed to git' : '';
          const skipMsg = skippedCount > 0
            ? ' (' + skippedCount + ' deselected and left for next time)'
            : '';
          this.toast('info', 'Export committed — ' + total + ' change' + (total === 1 ? '' : 's') + gitMsg + skipMsg + '.');
        }
      } catch (e) {
        this.toast('error', 'Export failed: ' + e.message);
      } finally {
        this.loading.export = false;
      }
    },

    // Open the inline editor for the note attached to the most recent
    // CHANGELOG.md section. Pre-fills with whatever text is in the
    // file right now (authoritative, survives page refresh).
    async openEditExportNote() {
      this.editingExportNote = true;
      this.editExportNoteDraft = this.lastExportNote || '';
      try {
        const r = await this.apiFetch('/api/export/last-note');
        if (r && typeof r.note === 'string') {
          this.editExportNoteDraft = r.note;
        }
      } catch (e) {
        // Fall back to the cached value — editing still works.
      }
    },

    cancelEditExportNote() {
      this.editingExportNote = false;
      this.editExportNoteDraft = '';
    },

    // Save the edited note. Rewrites the top CHANGELOG.md section and
    // creates a follow-up git commit. User pushes when ready.
    async saveEditExportNote() {
      this.savingExportNote = true;
      try {
        const r = await this.apiFetch('/api/export/last-note', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ note: this.editExportNoteDraft || '' }),
        });
        this.lastExportNote = (r && r.note) || '';
        this.editingExportNote = false;
        await this.loadChangelog();
        await this.loadRepoStatus();
        const gitMsg = r && r.git_committed ? ' — new git commit created, push when ready' : '';
        this.toast('info', 'Note updated' + gitMsg + '.');
      } catch (e) {
        this.toast('error', 'Note update failed: ' + e.message);
      } finally {
        this.savingExportNote = false;
      }
    },
  };
}
