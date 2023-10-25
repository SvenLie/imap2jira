# imap2jira
This tool reads a mailbox and search for mails in inbox. These mails are converted into JIRA tickets. You have to customize structure_new_issue.json and structure_add_comment.json for your workflow. Additionaly you have to create an .env file from .env.example and customize it to your choice
After creating a JIRA ticket the mail will be moved to an directory outside the general inbox (you have to specify in .env-File).
You can run this tool with docker (image and docker-compose are provided in this project).

### Mail without [Issue-1] in subject
A new issue is generated for this mail. You want to set the issue number into the subject of your answer in format `[ISSUENUMBER]` 

### Mail with [Issue-1] in subject
If this issue exists a comment will added to this issue