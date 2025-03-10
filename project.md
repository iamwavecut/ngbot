```mermaid
graph TD
    %% Nodes Definition
    A[Start] --> B[Initialize Configuration]
    B --> C[Load Environment Variables]
    C --> D[Configure Logging]
    D --> E[Initialize Database]
    E --> F{Is Database Migration Needed?}
    F -->|Yes| G[Run Migrations]
    F -->|No| H[Proceed to Service Initialization]
    G --> H
    H --> I[Initialize Bot API]
    I --> J[Set Up Handlers]
    J --> K[Register Event Handlers]
    K --> L[Start Receiving Updates]
    
    %% Event Handling Flow
    L --> M{Received Update?}
    M -->|Yes| N[Validate Update]
    M -->|No| L
    N --> O{Is Update Relevant?}
    O -->|Yes| P[Determine Update Type]
    O -->|No| L
    
    %% Update Type Handling
    P --> Q{Update Type?}
    Q -->|Message| R[Process Message]
    Q -->|Callback Query| S[Process Callback Query]
    Q -->|Chat Join Request| T[Process Join Request]
    Q -->|Other| U[Handle Other Update Types]
    
    %% Message Processing Flow
    R --> V[Check if User is Member]
    V --> W{Is Member?}
    W -->|Yes| X[Skip Processing]
    W -->|No| Y[Check Ban Status]
    Y --> Z{Is Banned?}
    Z -->|Yes| AA[Process Banned Message]
    Z -->|No| AB[Check Content]
    AB --> AC{Is Content Empty?}
    AC -->|Yes| X
    AC -->|No| AD[Check for Spam]
    AD --> AE{Is Spam?}
    AE -->|Yes| AF[Process Spam Message]
    AE -->|No| AG[Add User as Member]
    AG --> X
    
    %% Process Banned Message
    AA --> AH[Delete Message]
    AH --> AI[Ban User]
    AI --> X
    
    %% Process Spam Message
    AF --> AJ[Delete Message]
    AJ --> AK[Ban User]
    AK --> X
    
    %% Process Callback Query
    S --> AL[Validate Callback Data]
    AL --> AM{Is Challenge Callback?}
    AM -->|Yes| AN[Process Challenge]
    AM -->|No| AO[Ignore Callback]
    AN --> AP[Validate Challenge Attempt]
    AP --> AQ{Is Attempt Valid?}
    AQ -->|Yes| AR[Approve Join]
    AQ -->|No| AS[Reject Join]
    AR --> AT[Delete Challenge Message]
    AR --> AU[Approve Join Request]
    AU --> AV[Send Welcome Message]
    AV --> X
    AS --> AW[Delete Challenge Message]
    AS --> AX[Reject Join Request]
    AX --> AY[Send Rejection Message]
    AY --> X
    
    %% Process Join Request
    T --> AZ[Check if User is Banned]
    AZ --> BA{Is Banned?}
    BA -->|Yes| BB[Ban User]
    BA -->|No| BC[Send Challenge Message]
    BC --> BD[Wait for Response]
    BD --> BE{Is Response Correct?}
    BE -->|Yes| BF[Approve Join]
    BE -->|No| BG[Reject Join]
    BF --> BH[Delete Challenge Message]
    BF --> BI[Send Welcome Message]
    BI --> X
    BG --> BJ[Delete Challenge Message]
    BG --> BK[Send Rejection Message]
    BK --> X
    
    %% Other Update Types
    U --> BL[Handle Other Updates]
    BL --> X
    
    %% Styling
    classDef handler fill:#f9f,stroke:#333,stroke-width:2px;
    class A,B,C,D,E,F,G,H,I,J,K,L,M,N,O,P,Q,R,S,T,U,V,W,X,Y,Z,AA,AB,AC,AD,AE,AF,AG,AH,AI,AJ,AK,AL,AM,AN,AO,AP,AQ,AR,AS,AT,AU,AV,AW,AX,AY,AZ,BA,BB,BC,BD,BE,BF,BG,BH,BI,BJ,BK,BL class handler;
```