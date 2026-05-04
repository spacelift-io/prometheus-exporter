package main

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/spacelift-io/prometheus-exporter/client"
	"github.com/spacelift-io/prometheus-exporter/client/session"
	"github.com/spacelift-io/prometheus-exporter/logging"
)

type spaceliftCollector struct {
	ctx                                    context.Context
	logger                                 *zap.SugaredLogger
	client                                 client.Client
	scrapeTimeout                          time.Duration
	metricsRangeWindow                     time.Duration
	publicRunsPending                      *prometheus.Desc
	publicWorkersBusy                      *prometheus.Desc
	publicParallelism                      *prometheus.Desc
	workerPoolRunsPending                  *prometheus.Desc
	workerPoolWorkersBusy                  *prometheus.Desc
	workerPoolWorkers                      *prometheus.Desc
	workerPoolWorkersDrained               *prometheus.Desc
	currentBillingPeriodStart              *prometheus.Desc
	currentBillingPeriodEnd                *prometheus.Desc
	currentBillingPeriodUsedPrivateSeconds *prometheus.Desc
	currentBillingPeriodUsedPublicSeconds  *prometheus.Desc
	currentBillingPeriodUsedSeats          *prometheus.Desc
	currentStacksCountByState              *prometheus.Desc
	currentResourcesCountByDrift           *prometheus.Desc
	currentAvgStackSizeByResourceCount     *prometheus.Desc
	currentMaxStackSizeByResourceCount     *prometheus.Desc
	currentAverageRunDuration              *prometheus.Desc
	currentMedianRunDuration               *prometheus.Desc
	currentDriftDetectionCoverage          *prometheus.Desc
	currentResourcesCountByVendor          *prometheus.Desc
	currentHasStacks                       *prometheus.Desc
	currentHasRuns                         *prometheus.Desc
	publicUsers                            *prometheus.Desc
	publicIntentProjects                   *prometheus.Desc
	publicRunsSchedulable                  *prometheus.Desc
	workerPoolRunsSchedulable              *prometheus.Desc
	workerPoolUsers                        *prometheus.Desc
	workerPoolNotifications                *prometheus.Desc
	workerPoolIntentProjects               *prometheus.Desc
	workerPoolDriftDetectionRunLimit       *prometheus.Desc
	workerPoolManagedByK8sController       *prometheus.Desc
	currentBillingPeriodAllowedSeconds     *prometheus.Desc
	currentBillingPeriodAllowedSeats       *prometheus.Desc
	currentBillingPeriodUsedSeconds        *prometheus.Desc
	currentBillingPeriodUsedWorkers        *prometheus.Desc
	currentBillingPeriodPricePerSeat       *prometheus.Desc
	currentBillingPeriodPricePerWorker     *prometheus.Desc
	currentBillingPeriodPricePerRunMinute  *prometheus.Desc
	currentBillingPeriodSubscriptionPrice  *prometheus.Desc
	currentBillingPeriodUpcomingInvoice    *prometheus.Desc
	seatsLimit                             *prometheus.Desc
	seatsInUse                             *prometheus.Desc
	integrationsCount                      *prometheus.Desc
	auditTrailRetentionDays                *prometheus.Desc
	runLogRetentionDays                    *prometheus.Desc
	largestStackResources                  *prometheus.Desc
	runsNeedingApproval                    *prometheus.Desc
	runsRequiringAttention                 *prometheus.Desc
	runsRecentlyApproved                   *prometheus.Desc
	driftDetectionProposedRunsRecent       *prometheus.Desc
	driftDetectionSchedulesUpcoming        *prometheus.Desc
	doraAverageLeadTimeForChanges          *prometheus.Desc
	doraMedianLeadTimeForChanges           *prometheus.Desc
	doraAverageTimeToRecovery              *prometheus.Desc
	doraMedianTimeToRecovery               *prometheus.Desc
	doraChangeFailureRate                  *prometheus.Desc
	doraDeploymentFrequencyRate            *prometheus.Desc
	doraAverageLeadTimeForChangesRange     *prometheus.Desc
	doraMedianLeadTimeForChangesRange      *prometheus.Desc
	doraAverageTimeToMergeRange            *prometheus.Desc
	doraMedianTimeToMergeRange             *prometheus.Desc
	doraAverageTimeToRecoveryRange         *prometheus.Desc
	doraMedianTimeToRecoveryRange          *prometheus.Desc
	doraChangeFailureRateRange             *prometheus.Desc
	rangeNumberOfDeployments               *prometheus.Desc
	rangeStackFailures                     *prometheus.Desc
	rangeAverageRunDuration                *prometheus.Desc
	rangeMedianRunDuration                 *prometheus.Desc
	rangeResourcesCount                    *prometheus.Desc
	scrapeDuration                         *prometheus.Desc
	buildInfo                              *prometheus.Desc
}

func newSpaceliftCollector(ctx context.Context, httpClient *http.Client, session session.Session, scrapeTimeout, metricsRangeWindow time.Duration) (prometheus.Collector, error) {
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return nil, errors.New("could not read build info")
	}

	return &spaceliftCollector{
		ctx:                ctx,
		logger:             logging.FromContext(ctx).Sugar(),
		client:             client.New(httpClient, session),
		scrapeTimeout:      scrapeTimeout,
		metricsRangeWindow: metricsRangeWindow,
		publicRunsPending: prometheus.NewDesc(
			"spacelift_public_worker_pool_runs_pending",
			"The number of runs in your account currently queued and waiting for a public worker",
			nil,
			nil),
		publicWorkersBusy: prometheus.NewDesc(
			"spacelift_public_worker_pool_workers_busy",
			"The number of currently busy workers in the public worker pool for this account",
			nil,
			nil),
		publicParallelism: prometheus.NewDesc(
			"spacelift_public_worker_pool_parallelism",
			"The maximum number of simultaneously executing runs on the public worker pool for this account",
			nil,
			nil),
		workerPoolRunsPending: prometheus.NewDesc(
			"spacelift_worker_pool_runs_pending",
			"The number of runs currently queued and waiting for a worker from a particular pool",
			[]string{"worker_pool_id", "worker_pool_name"},
			nil),
		workerPoolWorkersBusy: prometheus.NewDesc(
			"spacelift_worker_pool_workers_busy",
			"The number of currently busy workers in a worker pool",
			[]string{"worker_pool_id", "worker_pool_name"},
			nil),
		workerPoolWorkers: prometheus.NewDesc(
			"spacelift_worker_pool_workers",
			"The number of workers in a worker pool",
			[]string{"worker_pool_id", "worker_pool_name"},
			nil),
		workerPoolWorkersDrained: prometheus.NewDesc(
			"spacelift_worker_pool_workers_drained",
			"The number of workers in a worker pool that have been drained",
			[]string{"worker_pool_id", "worker_pool_name"},
			nil),
		currentBillingPeriodStart: prometheus.NewDesc(
			"spacelift_current_billing_period_start_timestamp_seconds",
			"The timestamp of the start of the current billing period",
			nil,
			nil),
		currentBillingPeriodEnd: prometheus.NewDesc(
			"spacelift_current_billing_period_end_timestamp_seconds",
			"The timestamp of the end of the current billing period",
			nil,
			nil),
		currentBillingPeriodUsedPrivateSeconds: prometheus.NewDesc(
			"spacelift_current_billing_period_used_private_seconds",
			"The amount of private worker usage in the current billing period",
			nil,
			nil),
		currentBillingPeriodUsedPublicSeconds: prometheus.NewDesc(
			"spacelift_current_billing_period_used_public_seconds",
			"The amount of public worker usage in the current billing period",
			nil,
			nil),
		currentBillingPeriodUsedSeats: prometheus.NewDesc(
			"spacelift_current_billing_period_used_seats",
			"The number of seats used in the current billing period",
			nil,
			nil),
		currentStacksCountByState: prometheus.NewDesc(
			"spacelift_current_stacks_count_by_state",
			"The number of stacks grouped by state",
			[]string{"state"},
			nil),
		currentResourcesCountByDrift: prometheus.NewDesc(
			"spacelift_current_resources_count_by_drift",
			"The number of drifted resources",
			[]string{"state"},
			nil),
		currentAvgStackSizeByResourceCount: prometheus.NewDesc(
			"spacelift_current_avg_stack_size_by_resource_count",
			"The average stack size by resource count",
			nil,
			nil),
		currentAverageRunDuration: prometheus.NewDesc(
			"spacelift_current_average_run_duration",
			"The average run duration",
			nil,
			nil),
		currentMedianRunDuration: prometheus.NewDesc(
			"spacelift_current_median_run_duration",
			"The median run duration",
			nil,
			nil),
		currentMaxStackSizeByResourceCount: prometheus.NewDesc(
			"spacelift_current_max_stack_size_by_resource_count",
			"The maximum stack size by resource count",
			nil,
			nil),
		currentDriftDetectionCoverage: prometheus.NewDesc(
			"spacelift_current_drift_detection_coverage",
			"Drift detection coverage across stacks",
			nil,
			nil),
		currentResourcesCountByVendor: prometheus.NewDesc(
			"spacelift_current_resources_count_by_vendor",
			"The number of resources grouped by vendor",
			[]string{"vendor"},
			nil),
		currentHasStacks: prometheus.NewDesc(
			"spacelift_current_has_stacks",
			"Whether the account has any stacks (1) or not (0)",
			nil,
			nil),
		currentHasRuns: prometheus.NewDesc(
			"spacelift_current_has_runs",
			"Whether the account has any runs (1) or not (0)",
			nil,
			nil),
		publicUsers: prometheus.NewDesc(
			"spacelift_public_worker_pool_users",
			"The number of stacks/modules using the public worker pool",
			nil,
			nil),
		publicIntentProjects: prometheus.NewDesc(
			"spacelift_public_worker_pool_intent_projects",
			"The number of intent projects using the public worker pool",
			nil,
			nil),
		publicRunsSchedulable: prometheus.NewDesc(
			"spacelift_public_worker_pool_runs_schedulable",
			"The number of schedulable runs on the public worker pool for this account",
			nil,
			nil),
		workerPoolRunsSchedulable: prometheus.NewDesc(
			"spacelift_worker_pool_runs_schedulable",
			"The number of schedulable runs on a worker pool",
			[]string{"worker_pool_id", "worker_pool_name"},
			nil),
		workerPoolUsers: prometheus.NewDesc(
			"spacelift_worker_pool_users",
			"The number of stacks/modules using a worker pool",
			[]string{"worker_pool_id", "worker_pool_name"},
			nil),
		workerPoolNotifications: prometheus.NewDesc(
			"spacelift_worker_pool_notifications",
			"The number of new notifications on a worker pool",
			[]string{"worker_pool_id", "worker_pool_name"},
			nil),
		workerPoolIntentProjects: prometheus.NewDesc(
			"spacelift_worker_pool_intent_projects",
			"The number of intent projects using a worker pool",
			[]string{"worker_pool_id", "worker_pool_name"},
			nil),
		workerPoolDriftDetectionRunLimit: prometheus.NewDesc(
			"spacelift_worker_pool_drift_detection_run_limit",
			"Maximum number of drift detection runs that can be scheduled on a worker pool. Negative values mean no limit; not emitted when unset",
			[]string{"worker_pool_id", "worker_pool_name"},
			nil),
		workerPoolManagedByK8sController: prometheus.NewDesc(
			"spacelift_worker_pool_managed_by_k8s_controller",
			"Whether a worker pool is managed by the Kubernetes WorkerPool controller (1) or not (0)",
			[]string{"worker_pool_id", "worker_pool_name"},
			nil),
		currentBillingPeriodAllowedSeconds: prometheus.NewDesc(
			"spacelift_current_billing_period_allowed_seconds",
			"The number of seconds that can be used within the current billing period",
			nil,
			nil),
		currentBillingPeriodAllowedSeats: prometheus.NewDesc(
			"spacelift_current_billing_period_allowed_seats",
			"The number of seats allowed within the current billing period",
			nil,
			nil),
		currentBillingPeriodUsedSeconds: prometheus.NewDesc(
			"spacelift_current_billing_period_used_seconds",
			"The total amount of worker usage in the current billing period",
			nil,
			nil),
		currentBillingPeriodUsedWorkers: prometheus.NewDesc(
			"spacelift_current_billing_period_used_workers",
			"Maximum number of concurrent self-hosted workers in the current billing period",
			nil,
			nil),
		currentBillingPeriodPricePerSeat: prometheus.NewDesc(
			"spacelift_current_billing_period_price_per_seat_cents",
			"The unit amount (in cents) charged per seat monthly; not emitted when unset",
			nil,
			nil),
		currentBillingPeriodPricePerWorker: prometheus.NewDesc(
			"spacelift_current_billing_period_price_per_worker_cents",
			"The unit amount (in cents) charged per worker monthly; not emitted when unset",
			nil,
			nil),
		currentBillingPeriodPricePerRunMinute: prometheus.NewDesc(
			"spacelift_current_billing_period_price_per_run_minute_cents",
			"The unit amount (in cents) charged per run-minute; not emitted when unset",
			nil,
			nil),
		currentBillingPeriodSubscriptionPrice: prometheus.NewDesc(
			"spacelift_current_billing_period_subscription_price_cents",
			"The unit amount (in cents) charged monthly for the subscription; not emitted when unset",
			nil,
			nil),
		currentBillingPeriodUpcomingInvoice: prometheus.NewDesc(
			"spacelift_current_billing_period_upcoming_invoice_amount_cents",
			"Cost forecast (in cents) of the upcoming invoice",
			nil,
			nil),
		seatsLimit: prometheus.NewDesc(
			"spacelift_seats_limit",
			"The total number of seats available; -1 means unlimited",
			[]string{"type"},
			nil),
		seatsInUse: prometheus.NewDesc(
			"spacelift_seats_in_use",
			"The number of seats currently in use (instantaneous; cf. spacelift_current_billing_period_used_seats which is billing-period scoped)",
			[]string{"type"},
			nil),
		integrationsCount: prometheus.NewDesc(
			"spacelift_integrations_count",
			"The number of integrations grouped by type",
			[]string{"integration"},
			nil),
		auditTrailRetentionDays: prometheus.NewDesc(
			"spacelift_audit_trail_retention_days",
			"How many days built-in audit trails are stored",
			nil,
			nil),
		runLogRetentionDays: prometheus.NewDesc(
			"spacelift_run_log_retention_days",
			"How many days run logs are retained",
			nil,
			nil),
		largestStackResources: prometheus.NewDesc(
			"spacelift_largest_stack_resources",
			"Resource count for each of the account's largest stacks (top-N as returned by the API)",
			[]string{"stack_slug", "stack_name", "stack_state"},
			nil),
		runsNeedingApproval: prometheus.NewDesc(
			"spacelift_runs_needing_approval",
			"The number of runs currently needing approval",
			nil,
			nil),
		runsRequiringAttention: prometheus.NewDesc(
			"spacelift_runs_requiring_attention",
			"The number of runs requiring attention",
			nil,
			nil),
		runsRecentlyApproved: prometheus.NewDesc(
			"spacelift_runs_recently_approved",
			"The number of recently approved runs",
			nil,
			nil),
		driftDetectionProposedRunsRecent: prometheus.NewDesc(
			"spacelift_drift_detection_proposed_runs_recent",
			"The number of recent drift detection proposed runs",
			nil,
			nil),
		driftDetectionSchedulesUpcoming: prometheus.NewDesc(
			"spacelift_drift_detection_schedules_upcoming",
			"The number of upcoming drift detection schedules",
			nil,
			nil),
		doraAverageLeadTimeForChanges: prometheus.NewDesc(
			"spacelift_dora_average_lead_time_for_changes_seconds",
			"DORA: overall average lead time for changes (seconds), over the configured metrics-range-window",
			nil,
			nil),
		doraMedianLeadTimeForChanges: prometheus.NewDesc(
			"spacelift_dora_median_lead_time_for_changes_seconds",
			"DORA: overall median lead time for changes (seconds), over the configured metrics-range-window",
			nil,
			nil),
		doraAverageTimeToRecovery: prometheus.NewDesc(
			"spacelift_dora_average_time_to_recovery_seconds",
			"DORA: overall average time to recovery (seconds), over the configured metrics-range-window",
			nil,
			nil),
		doraMedianTimeToRecovery: prometheus.NewDesc(
			"spacelift_dora_median_time_to_recovery_seconds",
			"DORA: overall median time to recovery (seconds), over the configured metrics-range-window",
			nil,
			nil),
		doraChangeFailureRate: prometheus.NewDesc(
			"spacelift_dora_change_failure_rate",
			"DORA: change failure rate as ratio of failed to completed deployments, over the configured metrics-range-window",
			nil,
			nil),
		doraDeploymentFrequencyRate: prometheus.NewDesc(
			"spacelift_dora_deployment_frequency_rate",
			"DORA: deployment frequency rate (deployments per day), over the configured metrics-range-window",
			nil,
			nil),
		doraAverageLeadTimeForChangesRange: prometheus.NewDesc(
			"spacelift_dora_average_lead_time_for_changes_range_seconds",
			"DORA: latest daily data point of average lead time for changes (seconds)",
			nil,
			nil),
		doraMedianLeadTimeForChangesRange: prometheus.NewDesc(
			"spacelift_dora_median_lead_time_for_changes_range_seconds",
			"DORA: latest daily data point of median lead time for changes (seconds)",
			nil,
			nil),
		doraAverageTimeToMergeRange: prometheus.NewDesc(
			"spacelift_dora_average_time_to_merge_range_seconds",
			"DORA: latest daily data point of average time to merge (seconds)",
			nil,
			nil),
		doraMedianTimeToMergeRange: prometheus.NewDesc(
			"spacelift_dora_median_time_to_merge_range_seconds",
			"DORA: latest daily data point of median time to merge (seconds)",
			nil,
			nil),
		doraAverageTimeToRecoveryRange: prometheus.NewDesc(
			"spacelift_dora_average_time_to_recovery_range_seconds",
			"DORA: latest daily data point of average time to recovery (seconds)",
			nil,
			nil),
		doraMedianTimeToRecoveryRange: prometheus.NewDesc(
			"spacelift_dora_median_time_to_recovery_range_seconds",
			"DORA: latest daily data point of median time to recovery (seconds)",
			nil,
			nil),
		doraChangeFailureRateRange: prometheus.NewDesc(
			"spacelift_dora_change_failure_rate_range",
			"DORA: latest daily data point of change failure rate",
			nil,
			nil),
		rangeNumberOfDeployments: prometheus.NewDesc(
			"spacelift_number_of_deployments_range",
			"Latest daily data point of deployment count",
			nil,
			nil),
		rangeStackFailures: prometheus.NewDesc(
			"spacelift_stack_failures_range",
			"Latest daily data point of stack failure count",
			nil,
			nil),
		rangeAverageRunDuration: prometheus.NewDesc(
			"spacelift_average_run_duration_range",
			"Latest daily data point of average run duration",
			nil,
			nil),
		rangeMedianRunDuration: prometheus.NewDesc(
			"spacelift_median_run_duration_range",
			"Latest daily data point of median run duration",
			nil,
			nil),
		rangeResourcesCount: prometheus.NewDesc(
			"spacelift_resources_count_range",
			"Latest daily data point of total resources count",
			nil,
			nil),
		scrapeDuration: prometheus.NewDesc(
			"spacelift_scrape_duration_seconds",
			"The duration in seconds of the request to the Spacelift API for metrics",
			nil,
			nil),
		buildInfo: prometheus.NewDesc(
			"spacelift_build_info",
			"Contains build information about the exporter",
			nil,
			prometheus.Labels{"version": version, "commit": commit, "goversion": buildInfo.GoVersion}),
	}, nil
}

func (c *spaceliftCollector) Describe(descriptorChannel chan<- *prometheus.Desc) {
	descriptorChannel <- c.publicRunsPending
	descriptorChannel <- c.publicWorkersBusy
	descriptorChannel <- c.publicParallelism
	descriptorChannel <- c.workerPoolRunsPending
	descriptorChannel <- c.workerPoolWorkersBusy
	descriptorChannel <- c.workerPoolWorkersDrained
	descriptorChannel <- c.currentBillingPeriodStart
	descriptorChannel <- c.currentBillingPeriodEnd
	descriptorChannel <- c.currentBillingPeriodUsedPrivateSeconds
	descriptorChannel <- c.currentBillingPeriodUsedPublicSeconds
	descriptorChannel <- c.currentBillingPeriodUsedSeats
	descriptorChannel <- c.currentStacksCountByState
	descriptorChannel <- c.currentResourcesCountByDrift
	descriptorChannel <- c.currentAvgStackSizeByResourceCount
	descriptorChannel <- c.currentAverageRunDuration
	descriptorChannel <- c.currentMedianRunDuration
	descriptorChannel <- c.currentMaxStackSizeByResourceCount
	descriptorChannel <- c.currentDriftDetectionCoverage
	descriptorChannel <- c.currentResourcesCountByVendor
	descriptorChannel <- c.currentHasStacks
	descriptorChannel <- c.currentHasRuns
	descriptorChannel <- c.publicUsers
	descriptorChannel <- c.publicIntentProjects
	descriptorChannel <- c.publicRunsSchedulable
	descriptorChannel <- c.workerPoolRunsSchedulable
	descriptorChannel <- c.workerPoolUsers
	descriptorChannel <- c.workerPoolNotifications
	descriptorChannel <- c.workerPoolIntentProjects
	descriptorChannel <- c.workerPoolDriftDetectionRunLimit
	descriptorChannel <- c.workerPoolManagedByK8sController
	descriptorChannel <- c.currentBillingPeriodAllowedSeconds
	descriptorChannel <- c.currentBillingPeriodAllowedSeats
	descriptorChannel <- c.currentBillingPeriodUsedSeconds
	descriptorChannel <- c.currentBillingPeriodUsedWorkers
	descriptorChannel <- c.currentBillingPeriodPricePerSeat
	descriptorChannel <- c.currentBillingPeriodPricePerWorker
	descriptorChannel <- c.currentBillingPeriodPricePerRunMinute
	descriptorChannel <- c.currentBillingPeriodSubscriptionPrice
	descriptorChannel <- c.currentBillingPeriodUpcomingInvoice
	descriptorChannel <- c.seatsLimit
	descriptorChannel <- c.seatsInUse
	descriptorChannel <- c.integrationsCount
	descriptorChannel <- c.auditTrailRetentionDays
	descriptorChannel <- c.runLogRetentionDays
	descriptorChannel <- c.largestStackResources
	descriptorChannel <- c.runsNeedingApproval
	descriptorChannel <- c.runsRequiringAttention
	descriptorChannel <- c.runsRecentlyApproved
	descriptorChannel <- c.driftDetectionProposedRunsRecent
	descriptorChannel <- c.driftDetectionSchedulesUpcoming
	descriptorChannel <- c.doraAverageLeadTimeForChanges
	descriptorChannel <- c.doraMedianLeadTimeForChanges
	descriptorChannel <- c.doraAverageTimeToRecovery
	descriptorChannel <- c.doraMedianTimeToRecovery
	descriptorChannel <- c.doraChangeFailureRate
	descriptorChannel <- c.doraDeploymentFrequencyRate
	descriptorChannel <- c.doraAverageLeadTimeForChangesRange
	descriptorChannel <- c.doraMedianLeadTimeForChangesRange
	descriptorChannel <- c.doraAverageTimeToMergeRange
	descriptorChannel <- c.doraMedianTimeToMergeRange
	descriptorChannel <- c.doraAverageTimeToRecoveryRange
	descriptorChannel <- c.doraMedianTimeToRecoveryRange
	descriptorChannel <- c.doraChangeFailureRateRange
	descriptorChannel <- c.rangeNumberOfDeployments
	descriptorChannel <- c.rangeStackFailures
	descriptorChannel <- c.rangeAverageRunDuration
	descriptorChannel <- c.rangeMedianRunDuration
	descriptorChannel <- c.rangeResourcesCount
	descriptorChannel <- c.buildInfo
}

type dataPoint struct {
	Value  float64
	Labels []string
}

type metricsQuery struct {
	PublicWorkerPool struct {
		Parallelism          int `graphql:"parallelism"`
		BusyWorkers          int `graphql:"busyWorkers"`
		PendingRuns          int `graphql:"pendingRuns"`
		UsersCount           int `graphql:"usersCount"`
		IntentProjectsCount  int `graphql:"intentProjectsCount"`
		SchedulableRunsCount int `graphql:"schedulableRunsCount"`
	} `graphql:"publicWorkerPool"`
	WorkerPools []struct {
		ID                     string `graphql:"id"`
		Name                   string `graphql:"name"`
		PendingRuns            int    `graphql:"pendingRuns"`
		BusyWorkers            int    `graphql:"busyWorkers"`
		SchedulableRunsCount   int    `graphql:"schedulableRunsCount"`
		UsersCount             int    `graphql:"usersCount"`
		NotificationCount      int    `graphql:"notificationCount"`
		IntentProjectsCount    int    `graphql:"intentProjectsCount"`
		DriftDetectionRunLimit *int   `graphql:"driftDetectionRunLimit"`
		ManagedByK8sController bool   `graphql:"managedByK8sController"`
		Workers                []struct {
			ID      string `graphql:"id"`
			Drained bool   `graphql:"drained"`
		} `graphql:"workers"`
	} `graphql:"workerPools"`
	Usage struct {
		BillingPeriodStart         int  `graphql:"billingPeriodStart"`
		BillingPeriodEnd           int  `graphql:"billingPeriodEnd"`
		UsedPrivateMinutes         int  `graphql:"usedPrivateMinutes"`
		UsedPublicMinutes          int  `graphql:"usedPublicMinutes"`
		UsedSeats                  int  `graphql:"usedSeats"`
		AllowedMinutes             int  `graphql:"allowedMinutes"`
		AllowedSeats               int  `graphql:"allowedSeats"`
		UsedMinutes                int  `graphql:"usedMinutes"`
		UsedWorkers                int  `graphql:"usedWorkers"`
		PricePerSeat               *int `graphql:"pricePerSeat"`
		PricePerWorker             *int `graphql:"pricePerWorker"`
		PricePerRunMinute          *int `graphql:"pricePerRunMinute"`
		SubscriptionPrice          *int `graphql:"subscriptionPrice"`
		UpcomingInvoiceTotalAmount int  `graphql:"upcomingInvoiceTotalAmount"`
	} `graphql:"usage"`
	Seats struct {
		User struct {
			Limit int `graphql:"limit"`
			InUse int `graphql:"inUse"`
		} `graphql:"user"`
		APIKey struct {
			Limit int `graphql:"limit"`
			InUse int `graphql:"inUse"`
		} `graphql:"apiKey"`
	} `graphql:"seats"`
	IntegrationsCount struct {
		AWS                 int `graphql:"aws"`
		Azure               int `graphql:"azure"`
		AzureDevOps         int `graphql:"azureDevOps"`
		Backstage           int `graphql:"backstage"`
		BitbucketCloud      int `graphql:"bitbucketCloud"`
		BitbucketDatacenter int `graphql:"bitbucketDatacenter"`
		Github              int `graphql:"github"`
		GitLab              int `graphql:"gitlab"`
		ServiceNow          int `graphql:"serviceNow"`
		Slack               int `graphql:"slack"`
		VCSAgentPools       int `graphql:"vcsAgentPools"`
		Webhooks            int `graphql:"webhooks"`
		AI                  int `graphql:"ai"`
	} `graphql:"integrationsCount"`
	AuditTrailRetentionDays int `graphql:"auditTrailRetentionDays"`
	RunLogRetentionDays     int `graphql:"runLogRetentionDays"`
	Metrics                 struct {
		StacksCountByState          []dataPoint `graphql:"stacksCountByState"`
		ResourcesCountByDrift       []dataPoint `graphql:"resourcesCountByDrift"`
		AvgStackSizeByResourceCount []dataPoint `graphql:"avgStackSizeByResourceCount"`
		MaxStackSizeByResourceCount []dataPoint `graphql:"maxStackSizeByResourceCount"`
		AverageRunDuration          []dataPoint `graphql:"averageRunDuration"`
		MedianRunDuration           []dataPoint `graphql:"medianRunDuration"`
		DriftDetectionCoverage      []dataPoint `graphql:"driftDetectionCoverage"`
		ResourcesCountByVendor      []dataPoint `graphql:"resourcesCountByVendor"`
		HasStacks                   bool        `graphql:"hasStacks"`
		HasRuns                     bool        `graphql:"hasRuns"`
		LargestStacks               []struct {
			StackTile struct {
				Name  string `graphql:"name"`
				Slug  string `graphql:"slug"`
				State string `graphql:"state"`
			} `graphql:"stackTile"`
			ResourcesCount int `graphql:"resourcesCount"`
		} `graphql:"largestStacks"`
		NeedsApprovalRuns []struct {
			ID string `graphql:"id"`
		} `graphql:"needsApprovalRuns"`
		RunsRequiringAttention []struct {
			ID string `graphql:"id"`
		} `graphql:"runsRequiringAttention"`
		RunsRecentlyApproved []struct {
			Approval bool `graphql:"approval"`
		} `graphql:"runsRecentlyApproved"`
		RecentDriftDetectionProposedRuns []struct {
			ID string `graphql:"id"`
		} `graphql:"recentDriftDetectionProposedRuns"`
		UpcomingDriftDetectionSchedules []struct {
			StackTile struct {
				Slug string `graphql:"slug"`
			} `graphql:"stackTile"`
		} `graphql:"upcomingDriftDetectionSchedules"`
	} `graphql:"metrics"`
}

// metricsRangeQuery is queried independently so that a failure here
// (e.g. plan/feature gating) does not break the main metrics scrape.
type metricsRangeQuery struct {
	MetricsRange struct {
		AverageLeadTimeForChangesValue []dataPoint `graphql:"averageLeadTimeForChangesValue"`
		MedianLeadTimeForChangesValue  []dataPoint `graphql:"medianLeadTimeForChangesValue"`
		AverageTimeToRecoveryValue     []dataPoint `graphql:"averageTimeToRecoveryValue"`
		MedianTimeToRecoveryValue      []dataPoint `graphql:"medianTimeToRecoveryValue"`
		ChangeFailureRateValue         []dataPoint `graphql:"changeFailureRateValue"`
		DeploymentFrequencyRateValue   []dataPoint `graphql:"deploymentFrequencyRateValue"`
		AverageLeadTimeForChangesRange []dataPoint `graphql:"averageLeadTimeForChangesRange"`
		MedianLeadTimeForChangesRange  []dataPoint `graphql:"medianLeadTimeForChangesRange"`
		AverageTimeToMergeRange        []dataPoint `graphql:"averageTimeToMergeRange"`
		MedianTimeToMergeRange         []dataPoint `graphql:"medianTimeToMergeRange"`
		AverageTimeToRecoveryRange     []dataPoint `graphql:"averageTimeToRecoveryRange"`
		MedianTimeToRecoveryRange      []dataPoint `graphql:"medianTimeToRecoveryRange"`
		ChangeFailureRateRange         []dataPoint `graphql:"changeFailureRateRange"`
		NumberOfDeploymentsRange       []dataPoint `graphql:"numberOfDeploymentsRange"`
		StackFailuresRange             []dataPoint `graphql:"stackFailuresRange"`
		AverageRunDurationRange        []dataPoint `graphql:"averageRunDurationRange"`
		MedianRunDurationRange         []dataPoint `graphql:"medianRunDurationRange"`
		ResourcesCountRange            []dataPoint `graphql:"resourcesCountRange"`
	} `graphql:"metricsRange(input: $input)"`
}

// MetricsInput mirrors the GraphQL `MetricsInput` type for the metricsRange query.
type MetricsInput struct {
	StartTime int `json:"startTime"`
	EndTime   int `json:"endTime"`
}

func emitFirstDataPoint(ch chan<- prometheus.Metric, desc *prometheus.Desc, points []dataPoint) {
	if len(points) > 0 {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, points[0].Value)
	}
}

// emitLatestDataPoint emits the most recent data point in a *Range series, assuming
// chronological order from the API.
func emitLatestDataPoint(ch chan<- prometheus.Metric, desc *prometheus.Desc, points []dataPoint) {
	if len(points) > 0 {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, points[len(points)-1].Value)
	}
}

func emitOptionalInt(ch chan<- prometheus.Metric, desc *prometheus.Desc, v *int, labels ...string) {
	if v != nil {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, float64(*v), labels...)
	}
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func (c *spaceliftCollector) Collect(metricChannel chan<- prometheus.Metric) {
	var query metricsQuery

	start := time.Now()
	err := func() error {
		// The reason this is wrapped in an anonymous function is to ensure
		// that the cancel function is called immediately after the query completes.

		ctx, cancel := context.WithTimeout(c.ctx, c.scrapeTimeout)
		defer cancel()
		return c.client.Query(ctx, &query, nil)
	}()

	scrapeDuration := time.Since(start)
	metricChannel <- prometheus.MustNewConstMetric(c.scrapeDuration, prometheus.GaugeValue, scrapeDuration.Seconds())

	if err != nil {
		msg := "Failed to request metrics from the Spacelift API"
		if err == context.DeadlineExceeded {
			msg = "The request to the Spacelift API for metric data timed out"
		}

		c.logger.Errorw(msg, zap.Error(err))
		metricChannel <- prometheus.NewInvalidMetric(
			prometheus.NewDesc(
				"spacelift_error",
				msg,
				nil,
				nil),
			err)

		return
	}

	metricChannel <- prometheus.MustNewConstMetric(c.buildInfo, prometheus.GaugeValue, 1)
	metricChannel <- prometheus.MustNewConstMetric(c.publicRunsPending, prometheus.GaugeValue, float64(query.PublicWorkerPool.PendingRuns))
	metricChannel <- prometheus.MustNewConstMetric(c.publicWorkersBusy, prometheus.GaugeValue, float64(query.PublicWorkerPool.BusyWorkers))
	metricChannel <- prometheus.MustNewConstMetric(c.publicParallelism, prometheus.GaugeValue, float64(query.PublicWorkerPool.Parallelism))
	metricChannel <- prometheus.MustNewConstMetric(c.publicUsers, prometheus.GaugeValue, float64(query.PublicWorkerPool.UsersCount))
	metricChannel <- prometheus.MustNewConstMetric(c.publicIntentProjects, prometheus.GaugeValue, float64(query.PublicWorkerPool.IntentProjectsCount))
	metricChannel <- prometheus.MustNewConstMetric(c.publicRunsSchedulable, prometheus.GaugeValue, float64(query.PublicWorkerPool.SchedulableRunsCount))
	metricChannel <- prometheus.MustNewConstMetric(c.currentBillingPeriodStart, prometheus.GaugeValue, float64(query.Usage.BillingPeriodStart))
	metricChannel <- prometheus.MustNewConstMetric(c.currentBillingPeriodEnd, prometheus.GaugeValue, float64(query.Usage.BillingPeriodEnd))
	metricChannel <- prometheus.MustNewConstMetric(c.currentBillingPeriodUsedPrivateSeconds, prometheus.GaugeValue, float64(query.Usage.UsedPrivateMinutes*60))
	metricChannel <- prometheus.MustNewConstMetric(c.currentBillingPeriodUsedPublicSeconds, prometheus.GaugeValue, float64(query.Usage.UsedPublicMinutes*60))
	metricChannel <- prometheus.MustNewConstMetric(c.currentBillingPeriodUsedSeats, prometheus.GaugeValue, float64(query.Usage.UsedSeats))
	metricChannel <- prometheus.MustNewConstMetric(c.currentBillingPeriodAllowedSeconds, prometheus.GaugeValue, float64(query.Usage.AllowedMinutes*60))
	metricChannel <- prometheus.MustNewConstMetric(c.currentBillingPeriodAllowedSeats, prometheus.GaugeValue, float64(query.Usage.AllowedSeats))
	metricChannel <- prometheus.MustNewConstMetric(c.currentBillingPeriodUsedSeconds, prometheus.GaugeValue, float64(query.Usage.UsedMinutes*60))
	metricChannel <- prometheus.MustNewConstMetric(c.currentBillingPeriodUsedWorkers, prometheus.GaugeValue, float64(query.Usage.UsedWorkers))
	metricChannel <- prometheus.MustNewConstMetric(c.currentBillingPeriodUpcomingInvoice, prometheus.GaugeValue, float64(query.Usage.UpcomingInvoiceTotalAmount))

	emitOptionalInt(metricChannel, c.currentBillingPeriodPricePerSeat, query.Usage.PricePerSeat)
	emitOptionalInt(metricChannel, c.currentBillingPeriodPricePerWorker, query.Usage.PricePerWorker)
	emitOptionalInt(metricChannel, c.currentBillingPeriodPricePerRunMinute, query.Usage.PricePerRunMinute)
	emitOptionalInt(metricChannel, c.currentBillingPeriodSubscriptionPrice, query.Usage.SubscriptionPrice)

	metricChannel <- prometheus.MustNewConstMetric(c.seatsLimit, prometheus.GaugeValue, float64(query.Seats.User.Limit), "user")
	metricChannel <- prometheus.MustNewConstMetric(c.seatsLimit, prometheus.GaugeValue, float64(query.Seats.APIKey.Limit), "api_key")
	metricChannel <- prometheus.MustNewConstMetric(c.seatsInUse, prometheus.GaugeValue, float64(query.Seats.User.InUse), "user")
	metricChannel <- prometheus.MustNewConstMetric(c.seatsInUse, prometheus.GaugeValue, float64(query.Seats.APIKey.InUse), "api_key")

	for _, ic := range []struct {
		label string
		value int
	}{
		{"aws", query.IntegrationsCount.AWS},
		{"azure", query.IntegrationsCount.Azure},
		{"azure_devops", query.IntegrationsCount.AzureDevOps},
		{"backstage", query.IntegrationsCount.Backstage},
		{"bitbucket_cloud", query.IntegrationsCount.BitbucketCloud},
		{"bitbucket_datacenter", query.IntegrationsCount.BitbucketDatacenter},
		{"github", query.IntegrationsCount.Github},
		{"gitlab", query.IntegrationsCount.GitLab},
		{"service_now", query.IntegrationsCount.ServiceNow},
		{"slack", query.IntegrationsCount.Slack},
		{"vcs_agent_pools", query.IntegrationsCount.VCSAgentPools},
		{"webhooks", query.IntegrationsCount.Webhooks},
		{"ai", query.IntegrationsCount.AI},
	} {
		metricChannel <- prometheus.MustNewConstMetric(c.integrationsCount, prometheus.GaugeValue, float64(ic.value), ic.label)
	}

	metricChannel <- prometheus.MustNewConstMetric(c.auditTrailRetentionDays, prometheus.GaugeValue, float64(query.AuditTrailRetentionDays))
	metricChannel <- prometheus.MustNewConstMetric(c.runLogRetentionDays, prometheus.GaugeValue, float64(query.RunLogRetentionDays))

	for _, state := range query.Metrics.StacksCountByState {
		if len(state.Labels) > 0 {
			metricChannel <- prometheus.MustNewConstMetric(c.currentStacksCountByState, prometheus.GaugeValue, state.Value, state.Labels[0])
		}
	}

	for _, state := range query.Metrics.ResourcesCountByDrift {
		if len(state.Labels) > 0 {
			metricChannel <- prometheus.MustNewConstMetric(c.currentResourcesCountByDrift, prometheus.GaugeValue, state.Value, state.Labels[0])
		}
	}

	emitFirstDataPoint(metricChannel, c.currentAvgStackSizeByResourceCount, query.Metrics.AvgStackSizeByResourceCount)
	emitFirstDataPoint(metricChannel, c.currentAverageRunDuration, query.Metrics.AverageRunDuration)
	emitFirstDataPoint(metricChannel, c.currentMedianRunDuration, query.Metrics.MedianRunDuration)
	emitFirstDataPoint(metricChannel, c.currentMaxStackSizeByResourceCount, query.Metrics.MaxStackSizeByResourceCount)
	emitFirstDataPoint(metricChannel, c.currentDriftDetectionCoverage, query.Metrics.DriftDetectionCoverage)

	for _, vendor := range query.Metrics.ResourcesCountByVendor {
		if len(vendor.Labels) > 0 {
			metricChannel <- prometheus.MustNewConstMetric(c.currentResourcesCountByVendor, prometheus.GaugeValue, vendor.Value, vendor.Labels[0])
		}
	}

	metricChannel <- prometheus.MustNewConstMetric(c.currentHasStacks, prometheus.GaugeValue, boolToFloat(query.Metrics.HasStacks))
	metricChannel <- prometheus.MustNewConstMetric(c.currentHasRuns, prometheus.GaugeValue, boolToFloat(query.Metrics.HasRuns))

	for _, ls := range query.Metrics.LargestStacks {
		metricChannel <- prometheus.MustNewConstMetric(
			c.largestStackResources, prometheus.GaugeValue, float64(ls.ResourcesCount),
			ls.StackTile.Slug, ls.StackTile.Name, ls.StackTile.State,
		)
	}

	metricChannel <- prometheus.MustNewConstMetric(c.runsNeedingApproval, prometheus.GaugeValue, float64(len(query.Metrics.NeedsApprovalRuns)))
	metricChannel <- prometheus.MustNewConstMetric(c.runsRequiringAttention, prometheus.GaugeValue, float64(len(query.Metrics.RunsRequiringAttention)))
	metricChannel <- prometheus.MustNewConstMetric(c.runsRecentlyApproved, prometheus.GaugeValue, float64(len(query.Metrics.RunsRecentlyApproved)))
	metricChannel <- prometheus.MustNewConstMetric(c.driftDetectionProposedRunsRecent, prometheus.GaugeValue, float64(len(query.Metrics.RecentDriftDetectionProposedRuns)))
	metricChannel <- prometheus.MustNewConstMetric(c.driftDetectionSchedulesUpcoming, prometheus.GaugeValue, float64(len(query.Metrics.UpcomingDriftDetectionSchedules)))

	c.collectMetricsRange(metricChannel)

	for _, workerPool := range query.WorkerPools {
		metricChannel <- prometheus.MustNewConstMetric(c.workerPoolRunsPending, prometheus.GaugeValue, float64(workerPool.PendingRuns), workerPool.ID, workerPool.Name)
		metricChannel <- prometheus.MustNewConstMetric(c.workerPoolWorkersBusy, prometheus.GaugeValue, float64(workerPool.BusyWorkers), workerPool.ID, workerPool.Name)
		metricChannel <- prometheus.MustNewConstMetric(c.workerPoolWorkers, prometheus.GaugeValue, float64(len(workerPool.Workers)), workerPool.ID, workerPool.Name)

		drained := 0
		for _, worker := range workerPool.Workers {
			if worker.Drained {
				drained++
			}
		}
		metricChannel <- prometheus.MustNewConstMetric(c.workerPoolWorkersDrained, prometheus.GaugeValue, float64(drained), workerPool.ID, workerPool.Name)
		metricChannel <- prometheus.MustNewConstMetric(c.workerPoolRunsSchedulable, prometheus.GaugeValue, float64(workerPool.SchedulableRunsCount), workerPool.ID, workerPool.Name)
		metricChannel <- prometheus.MustNewConstMetric(c.workerPoolUsers, prometheus.GaugeValue, float64(workerPool.UsersCount), workerPool.ID, workerPool.Name)
		metricChannel <- prometheus.MustNewConstMetric(c.workerPoolNotifications, prometheus.GaugeValue, float64(workerPool.NotificationCount), workerPool.ID, workerPool.Name)
		metricChannel <- prometheus.MustNewConstMetric(c.workerPoolIntentProjects, prometheus.GaugeValue, float64(workerPool.IntentProjectsCount), workerPool.ID, workerPool.Name)

		emitOptionalInt(metricChannel, c.workerPoolDriftDetectionRunLimit, workerPool.DriftDetectionRunLimit, workerPool.ID, workerPool.Name)
		metricChannel <- prometheus.MustNewConstMetric(c.workerPoolManagedByK8sController, prometheus.GaugeValue, boolToFloat(workerPool.ManagedByK8sController), workerPool.ID, workerPool.Name)
	}
}

// collectMetricsRange runs the metricsRange GraphQL query as an independent
// request so that errors here (plan/feature gating, permission edge cases, or
// transient API failures) do not break the main metrics scrape. On failure we
// log and return — DORA/range metrics are simply absent until the next scrape.
func (c *spaceliftCollector) collectMetricsRange(metricChannel chan<- prometheus.Metric) {
	var rangeQuery metricsRangeQuery

	err := func() error {
		ctx, cancel := context.WithTimeout(c.ctx, c.scrapeTimeout)
		defer cancel()
		end := time.Now().Unix()
		variables := map[string]interface{}{
			"input": MetricsInput{
				StartTime: int(end - int64(c.metricsRangeWindow.Seconds())),
				EndTime:   int(end),
			},
		}
		return c.client.Query(ctx, &rangeQuery, variables)
	}()
	if err != nil {
		c.logger.Warnw("Failed to fetch metricsRange data; DORA/range metrics will be absent for this scrape", zap.Error(err))
		return
	}

	emitFirstDataPoint(metricChannel, c.doraAverageLeadTimeForChanges, rangeQuery.MetricsRange.AverageLeadTimeForChangesValue)
	emitFirstDataPoint(metricChannel, c.doraMedianLeadTimeForChanges, rangeQuery.MetricsRange.MedianLeadTimeForChangesValue)
	emitFirstDataPoint(metricChannel, c.doraAverageTimeToRecovery, rangeQuery.MetricsRange.AverageTimeToRecoveryValue)
	emitFirstDataPoint(metricChannel, c.doraMedianTimeToRecovery, rangeQuery.MetricsRange.MedianTimeToRecoveryValue)
	emitFirstDataPoint(metricChannel, c.doraChangeFailureRate, rangeQuery.MetricsRange.ChangeFailureRateValue)
	emitFirstDataPoint(metricChannel, c.doraDeploymentFrequencyRate, rangeQuery.MetricsRange.DeploymentFrequencyRateValue)

	emitLatestDataPoint(metricChannel, c.doraAverageLeadTimeForChangesRange, rangeQuery.MetricsRange.AverageLeadTimeForChangesRange)
	emitLatestDataPoint(metricChannel, c.doraMedianLeadTimeForChangesRange, rangeQuery.MetricsRange.MedianLeadTimeForChangesRange)
	emitLatestDataPoint(metricChannel, c.doraAverageTimeToMergeRange, rangeQuery.MetricsRange.AverageTimeToMergeRange)
	emitLatestDataPoint(metricChannel, c.doraMedianTimeToMergeRange, rangeQuery.MetricsRange.MedianTimeToMergeRange)
	emitLatestDataPoint(metricChannel, c.doraAverageTimeToRecoveryRange, rangeQuery.MetricsRange.AverageTimeToRecoveryRange)
	emitLatestDataPoint(metricChannel, c.doraMedianTimeToRecoveryRange, rangeQuery.MetricsRange.MedianTimeToRecoveryRange)
	emitLatestDataPoint(metricChannel, c.doraChangeFailureRateRange, rangeQuery.MetricsRange.ChangeFailureRateRange)
	emitLatestDataPoint(metricChannel, c.rangeNumberOfDeployments, rangeQuery.MetricsRange.NumberOfDeploymentsRange)
	emitLatestDataPoint(metricChannel, c.rangeStackFailures, rangeQuery.MetricsRange.StackFailuresRange)
	emitLatestDataPoint(metricChannel, c.rangeAverageRunDuration, rangeQuery.MetricsRange.AverageRunDurationRange)
	emitLatestDataPoint(metricChannel, c.rangeMedianRunDuration, rangeQuery.MetricsRange.MedianRunDurationRange)
	emitLatestDataPoint(metricChannel, c.rangeResourcesCount, rangeQuery.MetricsRange.ResourcesCountRange)
}
