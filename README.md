# imap2jira
This tool reads a mailbox and search for mails without important (\\IMPORTANT). These mails are converted into JIRA tickets. You have to customize structure_new_issue.json and structure_add_comment.json for your workflow.

You can run this tool with docker (image and docker-compose are provided in this project)

### Mail without [Issue-1] in subject
A new issue is generated for this mail. You want to set the issue number into the subject of your answer in format `[ISSUENUMBER]` 

### Mail with [Issue-1] in subject
If this issue exists a comment will added to this issue