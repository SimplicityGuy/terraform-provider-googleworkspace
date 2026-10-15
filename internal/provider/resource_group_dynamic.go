package googleworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"

	"google.golang.org/api/cloudidentity/v1"
	cloudidentitybeta "google.golang.org/api/cloudidentity/v1beta1"
)

func resourceGroupDynamic() *schema.Resource {
	return &schema.Resource{
		Description: "Dynamic Group resource manages Google Workspace Dynamic Groups using the Cloud Identity API. " +
			"Dynamic groups automatically add or remove members based on user attributes. " +
			"This resource requires the `https://www.googleapis.com/auth/cloud-identity.groups` client scope.",

		CreateContext: resourceGroupDynamicCreate,
		ReadContext:   resourceGroupDynamicRead,
		UpdateContext: resourceGroupDynamicUpdate,
		DeleteContext: resourceGroupDynamicDelete,

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(10 * time.Minute),
			Update: schema.DefaultTimeout(10 * time.Minute),
			Delete: schema.DefaultTimeout(5 * time.Minute),
		},

		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},

		Schema: map[string]*schema.Schema{
			"name": {
				Description: "The resource name of the Group. Format: groups/{group_id}",
				Type:        schema.TypeString,
				Computed:    true,
			},
			"email": {
				Description: "The group's email address. If your account has multiple domains, " +
					"select the appropriate domain for the email address. The email must be unique.",
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"display_name": {
				Description: "The display name of the Group.",
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
			},
			"description": {
				Description:      "An extended description to help users determine the purpose of a Group.",
				Type:             schema.TypeString,
				Optional:         true,
				ValidateDiagFunc: validation.ToDiagFunc(validation.StringLenBetween(0, 4096)),
			},
			"query": {
				Description: "The dynamic membership query for this group. " +
					"Members are automatically added or removed based on this query. " +
					"See https://cloud.google.com/identity/docs/reference/rest/v1/groups.memberships#DynamicGroupQuery",
				Type:     schema.TypeString,
				Required: true,
			},
			"security_group": {
				Description: "If true, adds the cloudidentity.googleapis.com/groups.security label to the group. " +
					"This is an immutable change - once added, the security label cannot be removed.",
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"locked": {
				Description: "If true, locks the group by adding the cloudidentity.googleapis.com/groups.locked label. " +
					"Locked groups prevent members from being added or removed. This can be toggled on/off.",
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"labels": {
				Description: "Additional custom label entries that apply to the Group. " +
					"The system labels (dynamic, discussion_forum, security, locked) are managed automatically or via their respective fields. " +
					"The 'dynamic' label is automatically added by the API, and 'discussion_forum' is added by default. " +
					"All label values must be empty strings.",
				Type:     schema.TypeMap,
				Optional: true,
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
			},
			"group_key_id": {
				Description: "The ID of the entity group key.",
				Type:        schema.TypeString,
				Computed:    true,
			},
			"group_key_namespace": {
				Description: "The namespace of the entity group key.",
				Type:        schema.TypeString,
				Computed:    true,
			},
			"create_time": {
				Description: "The time when the Group was created.",
				Type:        schema.TypeString,
				Computed:    true,
			},
			"update_time": {
				Description: "The time when the Group was last updated.",
				Type:        schema.TypeString,
				Computed:    true,
			},
		},
	}
}

func resourceGroupDynamicCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics

	client := meta.(*apiClient)
	email := d.Get("email").(string)

	log.Printf("[DEBUG] Creating Dynamic Group %q", email)

	cloudIdentityService, diags := client.NewCloudIdentityService()
	if diags.HasError() {
		return diags
	}

	groupsService, diags := GetCloudIdentityGroupsService(cloudIdentityService)
	if diags.HasError() {
		return diags
	}

	// Validate the dynamic query before creating
	query := d.Get("query").(string)
	validationDiags := validateDynamicGroupQuery(query)
	if validationDiags.HasError() {
		return validationDiags
	}

	// Build the group object with labels
	// Note: Do NOT set the dynamic label directly - it's automatically added by the API
	// However, discussion_forum label is required for all groups
	labels := map[string]string{
		"cloudidentity.googleapis.com/groups.discussion_forum": "",
	}

	// Add security label if requested (immutable once added)
	if d.Get("security_group").(bool) {
		labels["cloudidentity.googleapis.com/groups.security"] = ""
	}

	// Add locked label if requested
	if d.Get("locked").(bool) {
		labels["cloudidentity.googleapis.com/groups.locked"] = ""
	}

	// Add any custom labels
	if labelsRaw, ok := d.GetOk("labels"); ok {
		for k, v := range labelsRaw.(map[string]interface{}) {
			labels[k] = v.(string)
		}
	}

	group := &cloudidentity.Group{
		GroupKey: &cloudidentity.EntityKey{
			Id: email,
		},
		Parent: fmt.Sprintf("customers/%s", client.Customer),
		Labels: labels,
		DynamicGroupMetadata: &cloudidentity.DynamicGroupMetadata{
			Queries: []*cloudidentity.DynamicGroupQuery{
				{
					ResourceType: "USER",
					Query:        query,
				},
			},
		},
	}

	// Set display name if provided
	if displayName, ok := d.GetOk("display_name"); ok {
		group.DisplayName = displayName.(string)
	} else {
		// Default to email if not provided
		group.DisplayName = email
	}

	// Set description if provided
	if description, ok := d.GetOk("description"); ok {
		group.Description = description.(string)
	}

	// Create the group
	// Note: Dynamic groups must use EMPTY config, not WITH_INITIAL_OWNER
	// because membership is controlled entirely by the query
	operation, err := groupsService.Create(group).InitialGroupConfig("EMPTY").Do()
	if err != nil {
		return diag.Errorf("failed to create dynamic group: %v", err)
	}

	// The Create operation returns a long-running operation
	// We need to extract the actual group from the operation response
	if !operation.Done {
		return diag.Errorf("create operation is not complete (this shouldn't happen for group creation)")
	}

	// Extract the group from the operation response
	// The Response field is a RawMessage (JSON) that we need to unmarshal
	var createdGroup cloudidentity.Group
	if operation.Response != nil {
		if err := json.Unmarshal(operation.Response, &createdGroup); err != nil {
			return diag.Errorf("failed to unmarshal group from create operation response: %v", err)
		}

		// Set the ID to the group name (format: groups/{group_id})
		d.SetId(createdGroup.Name)
		log.Printf("[DEBUG] Finished creating Dynamic Group %q with name: %s", email, createdGroup.Name)
	} else {
		return diag.Errorf("create operation response is nil")
	}

	return resourceGroupDynamicRead(ctx, d, meta)
}

func resourceGroupDynamicRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics

	client := meta.(*apiClient)

	log.Printf("[DEBUG] Reading Dynamic Group with name: %s", d.Id())

	cloudIdentityService, diags := client.NewCloudIdentityService()
	if diags.HasError() {
		return diags
	}

	groupsService, diags := GetCloudIdentityGroupsService(cloudIdentityService)
	if diags.HasError() {
		return diags
	}

	group, err := groupsService.Get(d.Id()).Do()
	if err != nil {
		return handleNotFoundError(err, d, d.Get("email").(string))
	}

	d.Set("name", group.Name)
	d.Set("display_name", group.DisplayName)
	d.Set("description", group.Description)
	d.Set("create_time", group.CreateTime)
	d.Set("update_time", group.UpdateTime)

	if group.GroupKey != nil {
		d.Set("email", group.GroupKey.Id)
		d.Set("group_key_id", group.GroupKey.Id)
		d.Set("group_key_namespace", group.GroupKey.Namespace)
	}

	// Extract the dynamic query
	if group.DynamicGroupMetadata != nil && len(group.DynamicGroupMetadata.Queries) > 0 {
		d.Set("query", group.DynamicGroupMetadata.Queries[0].Query)
	}

	// Check for system labels and set boolean flags
	if group.Labels != nil {
		// Check for security label
		if _, ok := group.Labels["cloudidentity.googleapis.com/groups.security"]; ok {
			d.Set("security_group", true)
		} else {
			d.Set("security_group", false)
		}

		// Check for locked label
		if _, ok := group.Labels["cloudidentity.googleapis.com/groups.locked"]; ok {
			d.Set("locked", true)
		} else {
			d.Set("locked", false)
		}

		// Set custom labels (filter out the system labels)
		customLabels := make(map[string]string)
		systemLabels := map[string]bool{
			"cloudidentity.googleapis.com/groups.dynamic":          true,
			"cloudidentity.googleapis.com/groups.discussion_forum": true,
			"cloudidentity.googleapis.com/groups.security":         true,
			"cloudidentity.googleapis.com/groups.locked":           true,
		}

		for k, v := range group.Labels {
			if !systemLabels[k] {
				customLabels[k] = v
			}
		}

		if len(customLabels) > 0 {
			d.Set("labels", customLabels)
		}
	}

	return diags
}

func resourceGroupDynamicUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics

	client := meta.(*apiClient)

	log.Printf("[DEBUG] Updating Dynamic Group with name: %s", d.Id())

	cloudIdentityService, diags := client.NewCloudIdentityService()
	if diags.HasError() {
		return diags
	}

	groupsService, diags := GetCloudIdentityGroupsService(cloudIdentityService)
	if diags.HasError() {
		return diags
	}

	// Get the current group
	group, err := groupsService.Get(d.Id()).Do()
	if err != nil {
		return diag.FromErr(fmt.Errorf("failed to get group for update: %v", err))
	}

	var updateMask []string

	if d.HasChange("display_name") {
		group.DisplayName = d.Get("display_name").(string)
		updateMask = append(updateMask, "displayName")
	}

	if d.HasChange("description") {
		group.Description = d.Get("description").(string)
		updateMask = append(updateMask, "description")
	}

	// The membership query cannot be changed through v1: groups.patch there
	// only honors display_name, description and labels, and silently ignores a
	// dynamicGroupMetadata mask while still returning a successful Operation.
	// v1beta1 groups.patch accepts `dynamic_group_metadata`, so the query goes
	// through a separate call on that surface.
	if d.HasChange("query") {
		query := d.Get("query").(string)

		// Validate the new query
		validationDiags := validateDynamicGroupQuery(query)
		if validationDiags.HasError() {
			return validationDiags
		}

		betaService, betaDiags := client.NewCloudIdentityBetaService()
		if betaDiags.HasError() {
			return betaDiags
		}

		betaGroup := &cloudidentitybeta.Group{
			DynamicGroupMetadata: &cloudidentitybeta.DynamicGroupMetadata{
				Queries: []*cloudidentitybeta.DynamicGroupQuery{
					{
						ResourceType: "USER",
						Query:        query,
					},
				},
			},
		}
		operation, err := betaService.Groups.Patch(d.Id(), betaGroup).UpdateMask("dynamic_group_metadata").Do()
		if err != nil {
			return diag.FromErr(fmt.Errorf("failed to update dynamic group query: %v", err))
		}
		if operation != nil && operation.Error != nil {
			return diag.Errorf("failed to update dynamic group query: operation returned error %d: %s",
				operation.Error.Code, operation.Error.Message)
		}
		log.Printf("[DEBUG] Submitted query update for Dynamic Group %s via v1beta1", d.Id())
	}

	// Handle label changes (security_group, locked, and custom labels)
	if d.HasChange("security_group") || d.HasChange("locked") || d.HasChange("labels") {
		// Build labels map (do NOT include dynamic label - it's automatic)
		// However, discussion_forum label is required for all groups
		labels := map[string]string{
			"cloudidentity.googleapis.com/groups.discussion_forum": "",
		}

		// Add security label if set (note: this is immutable once added)
		if d.Get("security_group").(bool) {
			labels["cloudidentity.googleapis.com/groups.security"] = ""
		}

		// Add or remove locked label
		if d.Get("locked").(bool) {
			labels["cloudidentity.googleapis.com/groups.locked"] = ""
		}

		// Add custom labels
		if labelsRaw, ok := d.GetOk("labels"); ok {
			for k, v := range labelsRaw.(map[string]interface{}) {
				labels[k] = v.(string)
			}
		}

		group.Labels = labels
		updateMask = append(updateMask, "labels")
	}

	if len(updateMask) > 0 {
		// groups.patch returns a long-running Operation. A patch the API accepts
		// but does not apply (or applies with an error) still returns err == nil,
		// so the Operation itself has to be inspected and the result verified.
		operation, err := groupsService.Patch(d.Id(), group).UpdateMask(strings.Join(updateMask, ",")).Do()
		if err != nil {
			return diag.FromErr(fmt.Errorf("failed to update dynamic group: %v", err))
		}
		if operation != nil && operation.Error != nil {
			return diag.Errorf("failed to update dynamic group: operation returned error %d: %s",
				operation.Error.Code, operation.Error.Message)
		}
		if operation != nil && !operation.Done {
			log.Printf("[DEBUG] Dynamic Group %s update operation %q still running; verifying live state", d.Id(), operation.Name)
		}
	}

	if len(updateMask) > 0 || d.HasChange("query") {
		log.Printf("[DEBUG] Finished updating Dynamic Group with name: %s", d.Id())

		// Confirm every changed field is reflected live before returning. The
		// message must contain "timed out while waiting" so retryTimeDuration
		// keeps polling instead of giving up on the first mismatch.
		want := dynamicGroupExpectations(d)
		err = retryTimeDuration(ctx, d.Timeout(schema.TimeoutUpdate), func() error {
			updatedGroup, retryErr := groupsService.Get(d.Id()).Do()
			if retryErr != nil {
				return fmt.Errorf("error checking group status: %v", retryErr)
			}
			if pending := dynamicGroupPendingFields(updatedGroup, want); len(pending) > 0 {
				return fmt.Errorf("timed out while waiting for dynamic group update to be reflected: %s not yet updated",
					strings.Join(pending, ", "))
			}
			return nil
		})

		if err != nil {
			return diag.FromErr(fmt.Errorf("dynamic group %s update was accepted but not reflected by the API: %v", d.Id(), err))
		}
	}

	return resourceGroupDynamicRead(ctx, d, meta)
}

func resourceGroupDynamicDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics

	client := meta.(*apiClient)

	log.Printf("[DEBUG] Deleting Dynamic Group with name: %s", d.Id())

	cloudIdentityService, diags := client.NewCloudIdentityService()
	if diags.HasError() {
		return diags
	}

	groupsService, diags := GetCloudIdentityGroupsService(cloudIdentityService)
	if diags.HasError() {
		return diags
	}

	_, err := groupsService.Delete(d.Id()).Do()
	if err != nil {
		return handleNotFoundError(err, d, d.Get("email").(string))
	}

	log.Printf("[DEBUG] Finished deleting Dynamic Group with name: %s", d.Id())

	return diags
}

// validateDynamicGroupQuery validates that a dynamic group query has the required structure
// dynamicGroupExpected holds the values an Update must see reflected by the
// API before it reports success. A nil pointer means the field did not change
// and is not checked.
type dynamicGroupExpected struct {
	DisplayName *string
	Description *string
	Query       *string
}

func dynamicGroupExpectations(d *schema.ResourceData) dynamicGroupExpected {
	var want dynamicGroupExpected
	if d.HasChange("display_name") {
		v := d.Get("display_name").(string)
		want.DisplayName = &v
	}
	if d.HasChange("description") {
		v := d.Get("description").(string)
		want.Description = &v
	}
	if d.HasChange("query") {
		v := d.Get("query").(string)
		want.Query = &v
	}
	return want
}

// dynamicGroupPendingFields returns the names of expected fields the live group
// does not yet reflect. Empty means the update has fully landed.
func dynamicGroupPendingFields(live *cloudidentity.Group, want dynamicGroupExpected) []string {
	var pending []string
	if live == nil {
		return []string{"group"}
	}
	if want.DisplayName != nil && live.DisplayName != *want.DisplayName {
		pending = append(pending, "display_name")
	}
	if want.Description != nil && live.Description != *want.Description {
		pending = append(pending, "description")
	}
	if want.Query != nil {
		liveQuery := ""
		if live.DynamicGroupMetadata != nil && len(live.DynamicGroupMetadata.Queries) > 0 {
			liveQuery = live.DynamicGroupMetadata.Queries[0].Query
		}
		if liveQuery != *want.Query {
			pending = append(pending, "query")
		}
	}
	return pending
}

func validateDynamicGroupQuery(query string) diag.Diagnostics {
	var diags diag.Diagnostics

	if query == "" {
		return diag.Errorf("query cannot be empty")
	}

	// Basic validation - query should contain field comparisons
	// Common patterns: user.field == 'value', user.field.contains('value')
	if !strings.Contains(query, "user.") && !strings.Contains(query, "resource.") {
		diags = append(diags, diag.Diagnostic{
			Severity: diag.Warning,
			Summary:  "Query may be invalid",
			Detail: "Dynamic group queries typically reference 'user.' or 'resource.' fields. " +
				"Example: user.organizations.exists(org, org.department == 'Engineering')",
		})
	}

	// Check for some common operators
	hasOperator := false
	operators := []string{"==", "!=", "contains", "exists", "in", "&&", "||"}
	for _, op := range operators {
		if strings.Contains(query, op) {
			hasOperator = true
			break
		}
	}

	if !hasOperator {
		diags = append(diags, diag.Diagnostic{
			Severity: diag.Warning,
			Summary:  "Query may be missing operators",
			Detail: "Dynamic group queries typically use operators like ==, !=, contains, exists, in, &&, ||. " +
				"Example: user.email.contains('@example.com')",
		})
	}

	return diags
}
