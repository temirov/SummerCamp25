/* paste in DevTools → Console → Enter */
(() => {
    const getText = node =>
        node && node.textContent ? node.textContent.trim() : '';

    const currentUrl = window.location.href;            // ← page URL once

    const cards = [...document.querySelectorAll('.activity-card')];

    const sessions = cards.map(card => ({
        title: getText(card.querySelector('.activity-card-info__name')),
        dateRange: getText(
            card.querySelector('.activity-card-info__dateRange > span')
        ),
        dayTime: getText(card.querySelector('.activity-card-info__timeRange > span')),
        pageUrl: currentUrl                              // ← add to every payload
    }));

    console.table(sessions);
    copy(JSON.stringify(sessions, null, 2));
    console.info(
        `✅  ${sessions.length} session${sessions.length !== 1 ? 's' : ''} copied to clipboard`
    );
})();
