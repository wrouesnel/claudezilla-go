/**
 * Claudezilla Welcome Page
 * First-run onboarding experience
 */

// Check permission status on load
async function checkPermissionStatus() {
  const permissionBadge = document.getElementById('permissionStatus');

  try {
    const hasPermission = await browser.extension.isAllowedIncognitoAccess();

    if (hasPermission) {
      permissionBadge.classList.remove('disabled');
      permissionBadge.classList.add('enabled');
      // SECURITY: Build via DOM methods (no innerHTML — AMO UNSAFE_VAR_ASSIGNMENT)
      permissionBadge.replaceChildren();
      const dot = document.createElement('span');
      dot.className = 'dot';
      permissionBadge.appendChild(dot);
      permissionBadge.appendChild(document.createTextNode('Run in private windows: allowed ✓'));
    }
  } catch (e) {
    console.log('[claudezilla] Could not check permission status:', e);
  }
}

// Check permission status
checkPermissionStatus();

// Recheck every 2 seconds in case user enables permission. Stop polling once
// the permission flips to "enabled" so a long-open welcome tab doesn't keep
// the interval running forever.
const permissionPollId = setInterval(async () => {
  await checkPermissionStatus();
  const badge = document.getElementById('permissionStatus');
  if (badge && badge.classList.contains('enabled')) {
    clearInterval(permissionPollId);
  }
}, 2000);

