document.getElementById('planButton').addEventListener('click', async () => {
    const platform = document.getElementById('platform').value;
    const rancherVersion = document.getElementById('currentRancher').value;
    const k8sVersion = document.getElementById('currentK8s').value;

    if (!rancherVersion || !k8sVersion) {
        document.getElementById('planOutput').innerText =
            'Please enter both Rancher and Kubernetes versions.';
        return;
    }

    try {
        const response = await fetch(`/api/plan-upgrade/${platform}/${rancherVersion}/${k8sVersion}`);
        const result = await response.json();

        if (result.error) {
            document.getElementById('planOutput').innerText = `Error: ${result.error}`;
        } else if (!result.upgrade_path || result.upgrade_path.length === 0) {
            document.getElementById('planOutput').innerText = 'No upgrade path found for the provided input.';
        } else {
            const formattedPlan = formatUpgradePlan(result.upgrade_path);
            document.getElementById('planOutput').innerHTML = formattedPlan;
        }
    } catch (error) {
        document.getElementById('planOutput').innerText = 'Error fetching the upgrade plan. Please try again.';
    }
});

// Helper function to format the upgrade plan
function formatUpgradePlan(upgradePath) {
    let formatted = '';
    let firstRancherUpgrade = true; // Track the first Rancher upgrade step

    upgradePath.forEach((step) => {
        if (step.type === 'Rancher') {
            if (!firstRancherUpgrade) {
                formatted += `<br><hr><br>`; // Add separator between Rancher upgrades
            }
            formatted += `Rancher ${step.from} -> ${step.to}<br>`;
            firstRancherUpgrade = false; // Set the flag to false after the first step
        } else if (step.type === 'Kubernetes') {
            formatted += `${step.platform} ${step.from} -> ${step.to}<br>`;
        }
    });

    return formatted;
}
