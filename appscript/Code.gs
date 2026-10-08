// Starts Reach's GitHub Actions workflow (run.yml) each weekday morning, since GitHub's own
// `schedule` trigger is unreliable on new repos (it hadn't fired once for this one).
// Apps Script time triggers are dependable. Also adds a "Reach" menu to the approval Sheet
// for a run on demand.
//
// Setup (one-time, about 3 minutes):
//   1. Open the approval Sheet ("reach: approval queue") -> Extensions -> Apps Script,
//      replace the contents of Code.gs with this file, and save.
//   2. Project Settings (gear icon) -> Script Properties -> add:
//        GITHUB_PAT = <fine-grained PAT, Repository access: hksahni0-ux/reach only,
//                      Permissions: Actions: Read and write>
//   3. Run `installTrigger` once (function dropdown -> Run) and approve the Google prompt.
//   4. Done. `runReachIfDue` fires daily around 08:15 UK time and starts a run Monday to
//      Friday, at most once per day. Reload the Sheet to see the Reach menu.

var GITHUB_OWNER = 'hksahni0-ux';
var GITHUB_REPO = 'reach';
var WORKFLOW_FILE = 'run.yml';
var GITHUB_REF = 'main';
var TZ = 'Europe/London';

function today_() {
  return Utilities.formatDate(new Date(), TZ, 'yyyy-MM-dd');
}

function isWeekday_() {
  var day = Utilities.formatDate(new Date(), TZ, 'EEEE');
  return ['Saturday', 'Sunday'].indexOf(day) === -1;
}

// Returns the HTTP status (204 means GitHub accepted the run), or 0 if the PAT is missing.
function dispatchGithubWorkflow_() {
  var pat = PropertiesService.getScriptProperties().getProperty('GITHUB_PAT');
  if (!pat) {
    Logger.log('GITHUB_PAT script property is not set; see the setup notes at the top of this file.');
    return 0;
  }
  var url = 'https://api.github.com/repos/' + GITHUB_OWNER + '/' + GITHUB_REPO +
    '/actions/workflows/' + WORKFLOW_FILE + '/dispatches';
  var response = UrlFetchApp.fetch(url, {
    method: 'post',
    contentType: 'application/json',
    headers: { Authorization: 'Bearer ' + pat, Accept: 'application/vnd.github+json' },
    payload: JSON.stringify({ ref: GITHUB_REF }),
    muteHttpExceptions: true
  });
  var code = response.getResponseCode();
  Logger.log('GitHub dispatch response: %s %s', code, response.getContentText());
  if (code === 204) {
    PropertiesService.getScriptProperties().setProperty('LAST_RUN', today_());
  }
  return code;
}

// The daily trigger. Weekdays only, and never twice in one day (so a retried trigger
// can't spend the day's API allowances twice).
function runReachIfDue() {
  if (!isWeekday_()) {
    Logger.log('Weekend, skipping.');
    return;
  }
  if (PropertiesService.getScriptProperties().getProperty('LAST_RUN') === today_()) {
    Logger.log('Already ran today, skipping.');
    return;
  }
  dispatchGithubWorkflow_();
}

// Menu item: start a run now, whatever the day.
function runReachNow() {
  var code = dispatchGithubWorkflow_();
  var msg = code === 204 ? 'Reach started. New drafts appear here in about 5 minutes.'
    : code === 0 ? 'GITHUB_PAT is not set (Extensions -> Apps Script -> Project Settings -> Script Properties).'
    : 'GitHub refused the run (HTTP ' + code + '). Check the token is scoped to reach with Actions: Read and write.';
  SpreadsheetApp.getActive().toast(msg, 'Reach', 8);
}

function onOpen() {
  SpreadsheetApp.getUi().createMenu('Reach').addItem('Find posts now', 'runReachNow').addToUi();
}

function installTrigger() {
  ScriptApp.getProjectTriggers().forEach(function (t) {
    if (t.getHandlerFunction() === 'runReachIfDue') {
      ScriptApp.deleteTrigger(t);
    }
  });
  ScriptApp.newTrigger('runReachIfDue')
    .timeBased()
    .everyDays(1)
    .atHour(8)
    .nearMinute(15)
    .inTimezone(TZ)
    .create();
  Logger.log('Trigger installed: runReachIfDue fires daily around 08:15 UK time (runs on weekdays).');
}
